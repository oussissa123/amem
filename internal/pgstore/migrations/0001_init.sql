-- Agent Memory Core — initial schema (spec 02 §5.1).
-- Classes: D = derived (rebuilt from files by `amem index rebuild`), O = operational.

CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS unaccent;

-- Text search configurations: accent-insensitive simple, French and English.
CREATE TEXT SEARCH CONFIGURATION amem_simple (COPY = simple);
ALTER TEXT SEARCH CONFIGURATION amem_simple ALTER MAPPING FOR hword, hword_part, word WITH unaccent, simple;
CREATE TEXT SEARCH CONFIGURATION amem_fr (COPY = french);
ALTER TEXT SEARCH CONFIGURATION amem_fr ALTER MAPPING FOR hword, hword_part, word WITH unaccent, french_stem;
CREATE TEXT SEARCH CONFIGURATION amem_en (COPY = english);
ALTER TEXT SEARCH CONFIGURATION amem_en ALTER MAPPING FOR hword, hword_part, word WITH unaccent, english_stem;

-- O
CREATE TABLE projects (
  id          text PRIMARY KEY,
  name        text NOT NULL,
  root_path   text NOT NULL,
  config_hash text NOT NULL,
  archived_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- D
CREATE TABLE project_repositories (
  project_id  text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  remote_norm text NOT NULL UNIQUE,
  role        text,
  PRIMARY KEY (project_id, remote_norm)
);
CREATE TABLE project_external_refs (
  project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  system     text NOT NULL,
  ref        text NOT NULL,
  PRIMARY KEY (system, ref)
);

-- D
CREATE TABLE knowledge (
  id                text PRIMARY KEY,
  project_id        text NOT NULL REFERENCES projects(id),
  key               text NOT NULL,
  type              text NOT NULL,
  title             text NOT NULL,
  abstract          text NOT NULL,
  overview          text NOT NULL,
  topics            text[] NOT NULL DEFAULT '{}',
  components        text[] NOT NULL DEFAULT '{}',
  technologies      text[] NOT NULL DEFAULT '{}',
  keywords          text[] NOT NULL DEFAULT '{}',
  keywords_norm     text NOT NULL DEFAULT '',
  status            text NOT NULL,
  resolution        text,
  severity          text,
  confidence        text NOT NULL,
  classification    text NOT NULL,
  origin            text NOT NULL,
  supersedes_key    text,
  superseded_by_key text,
  source_refs       text[] NOT NULL DEFAULT '{}',
  version           int NOT NULL,
  content_hash      text NOT NULL,
  file_path         text NOT NULL,
  created_by        text NOT NULL,
  updated_by        text NOT NULL,
  created_at        timestamptz NOT NULL,
  updated_at        timestamptz NOT NULL,
  deleted_at        timestamptz,
  index_state       text NOT NULL DEFAULT 'lexical',
  UNIQUE (project_id, key)
);
CREATE INDEX knowledge_filter_idx ON knowledge (project_id, status, type);
CREATE INDEX knowledge_topics_idx ON knowledge USING gin (topics);
CREATE INDEX knowledge_components_idx ON knowledge USING gin (components);
CREATE INDEX knowledge_technologies_idx ON knowledge USING gin (technologies);
CREATE INDEX knowledge_keywords_trgm_idx ON knowledge USING gin (keywords_norm gin_trgm_ops);
CREATE INDEX knowledge_title_trgm_idx ON knowledge USING gin (title gin_trgm_ops);

-- D
CREATE TABLE knowledge_relations (
  project_id text NOT NULL,
  source_id  text NOT NULL REFERENCES knowledge(id) ON DELETE CASCADE,
  type       text NOT NULL,
  target_key text NOT NULL,
  PRIMARY KEY (source_id, type, target_key)
);
CREATE INDEX knowledge_relations_target_idx ON knowledge_relations (project_id, target_key);

-- D
CREATE TABLE chunks (
  id           bigserial PRIMARY KEY,
  project_id   text NOT NULL,
  knowledge_id text NOT NULL REFERENCES knowledge(id) ON DELETE CASCADE,
  version      int NOT NULL,
  ordinal      int NOT NULL,
  section      text NOT NULL,
  text         text NOT NULL,
  tsv          tsvector NOT NULL,
  token_count  int NOT NULL,
  text_hash    text NOT NULL
);
CREATE INDEX chunks_tsv_idx ON chunks USING gin (tsv);
CREATE INDEX chunks_knowledge_idx ON chunks (project_id, knowledge_id);

-- D
CREATE TABLE key_counters (
  project_id text NOT NULL,
  prefix     text NOT NULL,
  year       int NOT NULL,
  next       int NOT NULL,
  PRIMARY KEY (project_id, prefix, year)
);

-- D
CREATE TABLE project_maps (
  project_id   text PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
  content      text NOT NULL,
  token_count  int NOT NULL,
  generated_at timestamptz NOT NULL
);

-- O
CREATE TABLE principals (
  id          text PRIMARY KEY,
  kind        text NOT NULL,
  display     text NOT NULL,
  disabled_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE api_tokens (
  id           text PRIMARY KEY,
  principal_id text NOT NULL REFERENCES principals(id),
  prefix       text NOT NULL,
  hash         bytea NOT NULL UNIQUE,
  expires_at   timestamptz,
  revoked_at   timestamptz,
  last_used_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE grants (
  principal_id text NOT NULL REFERENCES principals(id),
  project_id   text NOT NULL REFERENCES projects(id),
  role         text NOT NULL,
  PRIMARY KEY (principal_id, project_id)
);

-- O
CREATE TABLE audit_log (
  id           bigserial PRIMARY KEY,
  at           timestamptz NOT NULL DEFAULT now(),
  project_id   text,
  principal_id text NOT NULL,
  action       text NOT NULL,
  target       text,
  version      int,
  details      jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_project_idx ON audit_log (project_id, at);

-- O
CREATE TABLE idempotency_keys (
  project_id   text NOT NULL,
  principal_id text NOT NULL,
  key          text NOT NULL,
  request_hash text NOT NULL,
  response     jsonb NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, principal_id, key)
);

-- O
CREATE TABLE search_log (
  id           text PRIMARY KEY,
  project_id   text NOT NULL,
  principal_id text NOT NULL,
  query        text,
  filters      jsonb,
  result_keys  text[],
  degraded     text[],
  latency_ms   int,
  at           timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE feedback (
  id            bigserial PRIMARY KEY,
  search_id     text,
  project_id    text NOT NULL,
  knowledge_key text NOT NULL,
  principal_id  text NOT NULL,
  verdict       text NOT NULL,
  comment       text,
  at            timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE ingestions (
  id           text PRIMARY KEY,
  project_id   text NOT NULL,
  principal_id text NOT NULL,
  kind         text NOT NULL,
  file_path    text NOT NULL,
  redactions   int NOT NULL,
  state        text NOT NULL,
  proposals    text[] NOT NULL DEFAULT '{}',
  context      jsonb NOT NULL DEFAULT '{}',
  expires_at   timestamptz NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now()
);
