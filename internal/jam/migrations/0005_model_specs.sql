-- Model-specs: named, harness-typed agent run descriptions (jam.ModelSpec),
-- one jsonb doc per name. Credentials appear by name only, never by value.
CREATE TABLE model_specs (
    name       text PRIMARY KEY,
    doc        jsonb NOT NULL,
    version    bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);
