#!/usr/bin/env python3
# See LICENSE.txt in the repository root for license information.
"""Offline reference-vector checker; NOT an Octop ingress or production verifier.

Requires Python 3 and OpenSSL Ed25519 support. Public synthetic proofs only;
no private keys, network, service credentials or third-party Python dependencies.
This deliberately models the proposed ordered Go JSON format, not JCS/JWT.
"""

import base64
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tempfile


def go_json(value):
    text = json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    for char in ("<", ">", "&", "\u2028", "\u2029"):
        text = text.replace(char, "\\u%04x" % ord(char))
    return text.encode("utf-8")


def canonical(raw, keys):
    value = json.loads(raw)
    assert isinstance(value, dict) and list(value) == keys
    assert go_json(value) == raw
    return value


def opaque(value):
    return isinstance(value, str) and 0 < len(value.encode("utf-8")) <= 128 and bool(value.strip())


def token(value):
    return isinstance(value, str) and re.fullmatch(r"[A-Za-z0-9_-]{1,128}", value) is not None


def verify(vector):
    raw = base64.b64decode(vector["Proof"], validate=True)
    p, now = vector["Policy"], vector["Now"]
    assert p["Enabled"] is True and p["OctopActorID"] > 0
    assert opaque(p["OctopOrganizationID"]) and opaque(p["OctopEmployeeID"])
    assert len(raw) <= 32768
    envelope = canonical(raw, ["payload", "signature"])
    payload = base64.b64decode(envelope["payload"], validate=True)
    c = canonical(payload, ["version", "key_id", "issued", "expires", "intent"])
    assert c["version"] == 2 and token(c["key_id"]) and c["key_id"] == p["KeyID"]
    i = c["intent"]
    assert list(i) == ["binding", "receipt", "receipt_expires_at", "goal", "mode"]
    b = i["binding"]
    assert list(b) == ["instance", "audience", "organization", "employee", "user", "team", "channel", "bot"]
    # Go Policy.Binding has the same lower-case JSON tags as Intent.Binding.
    assert b == p["Binding"]
    assert token(b["instance"]) and token(b["audience"])
    assert opaque(b["organization"]) and opaque(b["employee"])
    assert all(re.fullmatch(r"[a-z0-9]{26}", b[k]) for k in ("user", "team", "channel", "bot"))
    assert b["user"] != b["bot"] and re.fullmatch(r"[a-f0-9]{64}", i["receipt"])
    assert i["mode"] == "user_requested" and i["goal"].strip() and len(i["goal"]) <= 2000
    assert len(i["goal"].encode("utf-8")) <= 8000
    assert all(type(v) is int for v in (c["issued"], c["expires"], i["receipt_expires_at"]))
    assert 0 < c["issued"] <= now < c["expires"]
    assert c["expires"] - c["issued"] <= 120
    assert c["expires"] <= i["receipt_expires_at"] // 1000
    public = base64.b64decode(p["PublicKey"], validate=True)
    signature = base64.b64decode(envelope["signature"], validate=True)
    assert len(public) == 32 and len(signature) == 64
    # DER SubjectPublicKeyInfo for Ed25519 (RFC 8410); only public bytes are saved.
    with tempfile.TemporaryDirectory(prefix="handoff-public-vector-") as directory:
        root = Path(directory)
        (root / "public.der").write_bytes(bytes.fromhex("302a300506032b6570032100") + public)
        (root / "message").write_bytes(b"mattermost-octop-intent-proof/v2\0" + payload)
        (root / "signature").write_bytes(signature)
        result = subprocess.run(
            ["openssl", "pkeyutl", "-verify", "-pubin", "-keyform", "DER", "-inkey", str(root / "public.der"),
             "-rawin", "-in", str(root / "message"), "-sigfile", str(root / "signature")],
            capture_output=True, check=False, timeout=5,
        )
        assert result.returncode == 0
    identity = ["mattermost-octop-delivery/v1", b["instance"], b["audience"], i["receipt"]]
    fingerprint = {"Intent": i, "Actor": p["OctopActorID"], "Organization": p["OctopOrganizationID"], "Employee": p["OctopEmployeeID"]}
    return {
        "Intent": i, "OctopActorID": p["OctopActorID"],
        "OctopOrganizationID": p["OctopOrganizationID"], "OctopEmployeeID": p["OctopEmployeeID"],
        "DeliveryKey": hashlib.sha256(go_json(identity)).hexdigest(),
        "Fingerprint": hashlib.sha256(go_json(fingerprint)).hexdigest(),
    }


def main():
    vectors = json.loads(Path(__file__).with_name("vectors.json").read_text())
    for vector in vectors:
        try:
            result = verify(vector)
        except (AssertionError, ValueError, KeyError, TypeError, UnicodeError):
            assert vector["Accepted"] is False, vector["Name"]
        else:
            assert vector["Accepted"] is True and result == vector["Expected"], vector["Name"]
    print(f"{len(vectors)} public Go/Python/OpenSSL vectors passed; no service integration tested")


if __name__ == "__main__":
    main()
