"""Read-only, network-free source contract audit; never a readiness/connection probe.

Exit 2 means the reviewed peer remains incompatible. Exit 1 means source drift or
an audit failure. Exit 0 is reserved for --verify-known-blockers and only means
that the documented blockers were reproduced, NEVER that integration is enabled.
"""
from __future__ import annotations

import argparse
import ast
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parent


def git(git_dir: str, *args: str, allow_missing: bool = False) -> str:
    env = dict(os.environ, GIT_NO_LAZY_FETCH="1", GIT_TERMINAL_PROMPT="0", GIT_ALLOW_PROTOCOL="")
    result = subprocess.run(["git", "--git-dir=" + git_dir, *args], env=env,
                            capture_output=True, text=True, timeout=15)
    if result.returncode and not (allow_missing and result.returncode == 1):
        raise ValueError("local source inspection failed; no network fallback")
    return result.stdout


def models(source: str, wanted: list[str]) -> dict:
    result = {}
    for node in ast.parse(source).body:
        if isinstance(node, ast.ClassDef) and node.name in wanted:
            fields = {n.target.id: {"annotation": ast.unparse(n.annotation),
                                   "default_or_constraints": ast.unparse(n.value) if n.value else None,
                                   "required": n.value is None or (
                                       isinstance(n.value, ast.Call) and isinstance(n.value.func, ast.Name)
                                       and n.value.func.id == "Field" and not n.value.args
                                       and not any(k.arg in {"default", "default_factory"} for k in n.value.keywords))}
                      for n in node.body if isinstance(n, ast.AnnAssign) and isinstance(n.target, ast.Name)}
            configs = [n.value for n in node.body if isinstance(n, ast.Assign)
                       and any(isinstance(t, ast.Name) and t.id == "model_config" for t in n.targets)]
            config = ast.unparse(configs[0]) if configs else None
            result[node.name] = {"fields": fields, "config": config}
    if set(result) != set(wanted):
        raise ValueError("reviewed model missing")
    return result


def routes(source: str, router: str) -> list[dict]:
    tree = ast.parse(source)
    constants = {n.targets[0].id: n.value.value for n in tree.body
                 if isinstance(n, ast.Assign) and len(n.targets) == 1
                 and isinstance(n.targets[0], ast.Name)
                 and isinstance(n.value, ast.Constant) and isinstance(n.value.value, str)}

    def path(n: ast.AST) -> str:
        if isinstance(n, ast.Constant) and isinstance(n.value, str):
            return n.value
        if isinstance(n, ast.Name) and n.id in constants:
            return constants[n.id]
        if isinstance(n, ast.BinOp) and isinstance(n.op, ast.Add):
            return path(n.left) + path(n.right)
        raise ValueError("unreviewed route expression")

    result = []
    for node in tree.body:
        if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            continue
        for d in node.decorator_list:
            if (isinstance(d, ast.Call) and isinstance(d.func, ast.Attribute)
                    and isinstance(d.func.value, ast.Name) and d.func.value.id == router
                    and d.func.attr in {"get", "put", "post"}):
                result.append({"method": d.func.attr.upper(), "path": "/api" + path(d.args[0]),
                               "handler": node.name,
                               "declares_current_user": any(ast.unparse(v) == "Depends(current_user)"
                                                            for v in node.args.defaults)})
    return result


def inspect(git_dir: str, revision: str, manifest: dict) -> dict:
    if not re.fullmatch(r"[0-9a-f]{40}", revision) or revision != manifest["octop_revision"]:
        raise ValueError("unreviewed revision: update evidence through source review, not auto-accept")
    sources = {}
    for name, digest in manifest["source_sha256"].items():
        content = git(git_dir, "show", revision + ":" + name)
        if hashlib.sha256(content.encode()).hexdigest() != digest:
            raise ValueError("reviewed source checksum mismatch")
        sources[name] = content
    publication = sources["src/octop/api/routers/mattermost_publication.py"]
    worker = sources["src/octop/api/routers/digital_workers.py"]
    model_report = models(publication, ["PublicationPreviewBody", "PublicationDecisionBody", "PublicationRecord"])
    model_report.update(models(sources["src/octop/infra/digital_workers/mattermost.py"],
                               ["MattermostReadinessRequest", "MattermostIntentRequest"]))
    catalog = routes(publication, "router") + routes(worker, "_mattermost_router")
    if catalog != manifest["existing_routes"] or model_report != manifest["existing_models"]:
        raise ValueError("parsed contract drift")
    if not all(r["declares_current_user"] for r in catalog):
        raise ValueError("unreviewed authentication dependency")
    found = git(git_dir, "grep", "-F", "-l", "-e", "mattermost-octop-intent-proof/v2",
                "-e", "/integrations/mattermost/v1/intakes", revision, "--", "src", allow_missing=True)
    if found:
        raise ValueError("possible new peer ingress: manual review required")
    mismatch = []
    for name in ["PublicationPreviewBody", "PublicationDecisionBody", "MattermostReadinessRequest", "MattermostIntentRequest"]:
        model = model_report[name]
        if "extra='forbid'" not in model["config"] or "strict=True" not in model["config"]:
            raise ValueError("unreviewed body validation policy")
        mismatch.append({"model": name,
                         "unknown_proof_fields": sorted({"payload", "signature"} - set(model["fields"])),
                         "missing_required_fields": sorted(k for k,v in model["fields"].items() if v["required"]),
                         "extra_fields_forbidden": True})
    return {"audit": "known_incompatibility_reproduced", "octop_revision": revision,
            "proof_body_mismatches": mismatch,
            "compatible": False, "live_authentication_tested": False, "network_used": False,
            "existing_routes": catalog, "existing_models": model_report,
            "blockers": manifest["blockers"],
            "meaning": "Static source evidence only; no server, credentials, HTTP calls or execution."}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--git-dir", required=True, help="Existing local Octop .git or bare repository")
    parser.add_argument("--revision")
    parser.add_argument("--report")
    parser.add_argument("--verify-known-blockers", action="store_true")
    args = parser.parse_args()
    manifest = json.loads((ROOT / "octop-contract.json").read_text())
    try:
        report = inspect(args.git_dir, args.revision or manifest["octop_revision"], manifest)
    except (ValueError, OSError, subprocess.SubprocessError):
        print(json.dumps({"audit": "review_required", "compatible": False, "network_used": False}))
        return 1
    encoded = json.dumps(report, indent=2, ensure_ascii=False) + "\n"
    if args.report:
        Path(args.report).write_text(encoded)
    print(encoded, end="")
    return 0 if args.verify_known_blockers else 2


if __name__ == "__main__":
    raise SystemExit(main())
