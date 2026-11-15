-- Standalone OFFLINE PostgreSQL fixture schema, not an Octop migration.
-- No goal, proof, token or key material is stored. Do not run against production.
CREATE TABLE aitagent_intake (
 delivery_key text PRIMARY KEY CHECK (delivery_key ~ '^[a-f0-9]{64}$'),
 fingerprint text NOT NULL CHECK (fingerprint ~ '^[a-f0-9]{64}$'),
 organization_id text NOT NULL,
 employee_id text NOT NULL,
 actor_id bigint NOT NULL CHECK (actor_id > 0),
 source_expires_at bigint NOT NULL CHECK (source_expires_at > 0),
 state text NOT NULL CHECK (state IN ('pending_authorization','authorization_observed','execution_observed','blocked','cancelled')),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 8),
 source_version bigint NOT NULL DEFAULT 1 CHECK (source_version > 0)
);
CREATE TABLE aitagent_intake_audit (
 delivery_key text NOT NULL REFERENCES aitagent_intake(delivery_key),
 fingerprint text NOT NULL,
 action text NOT NULL CHECK (action IN ('pending_intake_recorded','authorization_observed','execution_observed','blocked','cancelled')),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 8),
 operation_id text NOT NULL DEFAULT 'intake',
 PRIMARY KEY(delivery_key,revision),
 UNIQUE(delivery_key,operation_id)
);
