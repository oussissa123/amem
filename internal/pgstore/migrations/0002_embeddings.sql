-- Phase 3 — semantic search (spec 02 §6.3).
-- Embeddings are content-addressed: chunks.embed_hash is the hash of the
-- embedding input, so a rebuild or a new version reuses unchanged vectors.
-- The `embeddings` table needs pgvector; it is created at runtime when an
-- embeddings provider is configured (pgstore.EnsureVector), so that a
-- database without the extension keeps working in lexical mode.

-- D
ALTER TABLE chunks ADD COLUMN embed_hash text NOT NULL DEFAULT '';
CREATE INDEX chunks_embed_hash_idx ON chunks (embed_hash);

-- D : un modèle d'embedding = une ligne ; dimension libre par modèle
CREATE TABLE embedding_models (
  id         smallserial PRIMARY KEY,
  provider   text NOT NULL,
  name       text NOT NULL,
  dims       int  NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, name, dims)
);
