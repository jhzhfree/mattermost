-- Standalone OFFLINE PostgreSQL fixture schema, not an Octop migration.
-- No goal, proof, token or key material is stored. Do not run against production.
CREATE TABLE aitagent_intake (
 delivery_key text PRIMARY KEY CHECK (delivery_key ~ '^[a-f0-9]{64}$'),
 fingerprint text NOT NULL CHECK (fingerprint ~ '^[a-f0-9]{64}$'),
 organization_id text NOT NULL,
 employee_id text NOT NULL,
 actor_id bigint NOT NULL CHECK (actor_id > 0),
 source_expires_at bigint NOT NULL CHECK (source_expires_at > 0),
 state text NOT NULL CHECK (state = 'pending_authorization')
);
CREATE TABLE aitagent_intake_audit (
 delivery_key text PRIMARY KEY REFERENCES aitagent_intake(delivery_key),
 fingerprint text NOT NULL,
 action text NOT NULL CHECK (action = 'pending_intake_recorded')
);
