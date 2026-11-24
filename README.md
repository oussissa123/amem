# Agent Memory Core (`amem`)

Mémoire durable **par projet** pour agents IA : incidents, décisions, conventions, architecture, procédures et leçons, partagées par tous les agents et développeurs d'un projet — qu'ils soient lancés par un orchestrateur (Multica) ou directement (Claude Code, Codex, autre client MCP).

Spécifications : [générale](docs/00-specification-generale.md) · [fonctionnelle](docs/01-specification-fonctionnelle.md) · [technique](docs/02-specification-technique.md).

## Installation avec Docker (serveur partagé)

Base PostgreSQL et serveur amem (REST `/v1`, MCP `/mcp`) sur `http://127.0.0.1:7420` :

```bash
docker compose up -d --build
docker compose exec amem amem init mra --name MRA --repo https://github.com/acme/mra-api
docker compose exec amem amem token create agent:multica/dev-senior   # jeton affiché une seule fois
docker compose exec amem amem grant agent:multica/dev-senior mra contributor
```

Les agents s'y branchent en mode HTTP (`adapter install … --mode http`). `AMEM_DB_PASSWORD` change le mot de passe de la base, `AMEM_HTTP_BIND=0.0.0.0` expose le serveur au réseau.

## Démarrage en local (binaire sur l'hôte)

```bash
make db        # PostgreSQL local sur 127.0.0.1:5433
make build     # produit bin/amem (Go tourne dans Docker)
```

```bash
bin/amem init mra --name MRA --description "Plateforme MRA" --repo git@github.com:acme/mra-api.git --repo https://github.com/acme/mra-mobile
```

Les fichiers canoniques sont dans `~/.local/share/amem/projects/<projet>/` (modifiable par `AMEM_ROOT`).

## Brancher les agents

**Multica** (banc d'essai prioritaire) — génère le `mcp_config` à coller dans la configuration de l'agent Multica et le bloc d'instructions :

```bash
bin/amem adapter install multica --agent "Développeur Senior"
```

Mode HTTP (serveur amem partagé, jeton par agent) :

```bash
bin/amem -p mra adapter install multica --agent "Développeur Senior" --mode http --server https://amem.interne
```

**Claude Code** (dans le dépôt) — écrit `.mcp.json` et un bloc dans `CLAUDE.md` :

```bash
bin/amem adapter install claude-code --dir ~/projets/mra/api
```

**Codex** — ajoute `[mcp_servers.amem]` à `~/.codex/config.toml` et un bloc dans `AGENTS.md` :

```bash
bin/amem adapter install codex --dir ~/projets/mra/api
```

Vérifier comme le ferait un agent (démarre `amem mcp`, appelle `memory_projects` et `memory_overview`) :

```bash
bin/amem adapter check --dir ~/projets/mra/api
```

Le projet est déduit du dépôt Git du répertoire de l'agent (ou de ses sous-répertoires, cas d'une tâche Multica à plusieurs dépôts) ; sinon `--project` / `AMEM_PROJECT` / en-tête `X-Amem-Project`.

## Utilisation en ligne de commande

```bash
bin/amem overview
bin/amem search "dial tcp 127.0.0.1:6379: connect: connection refused"
bin/amem read INC-2026-001
bin/amem record incident --from incident.md
bin/amem status DEC-2026-001 confirmed --reason "validé en revue"
bin/amem index rebuild -p mra
```

Identité locale : un humain au terminal est `human:<utilisateur>` (admin local) ; un processus lancé par un agent (Multica, Claude Code, Codex) est reconnu comme `agent:…` avec le rôle `contributor`, et ne peut donc pas confirmer ses propres connaissances. `AMEM_PRINCIPAL` force l'identité.

## Recherche sémantique

Sans configuration, amem cherche en mode lexical (correspondances exactes + plein texte FR/EN) et l'indique par `degraded: ["semantic"]`. Avec un fournisseur d'embeddings, un canal sémantique multilingue s'ajoute et les résultats sont fusionnés (RRF). La base doit avoir pgvector : l'image `db` du `docker-compose.yml` l'inclut (`docker compose up -d --build db` pour mettre à jour un volume existant).

Avec Ollama en local :

```bash
docker compose --profile ollama up -d ollama
docker compose exec ollama ollama pull bge-m3
AMEM_EMBEDDINGS_PROVIDER=ollama docker compose up -d amem
docker compose exec amem amem embed sync     # encode les connaissances existantes
docker compose exec amem amem index status -p mra
```

Binaire sur l'hôte : `AMEM_EMBEDDINGS_PROVIDER=ollama` (Ollama sur `http://localhost:11434`). Tout point de terminaison compatible OpenAI convient aussi :

```bash
export AMEM_EMBEDDINGS_PROVIDER=openai AMEM_EMBEDDINGS_MODEL=text-embedding-3-small AMEM_EMBEDDINGS_API_KEY_ENV=OPENAI_API_KEY
```

`amem serve` et `amem mcp` calculent les embeddings en arrière-plan après chaque écriture ; une panne du fournisseur ne bloque ni l'écriture ni la recherche lexicale. Les coefficients se règlent par projet dans `project.yaml` :

```yaml
search:
  weights: {lexical: 1.0, semantic: 1.0}
  semantic_min_similarity: 0.45   # à calibrer par modèle avec amem eval
  duplicate_threshold: 0.92       # RG-09
  freshness_half_life_days: 365
```

Mesurer la qualité sur un jeu de requêtes (et la comparer au rapport précédent) :

```yaml
# eval.yaml
project: mra
k: 3
cases:
  - query: "dial tcp 127.0.0.1:6379: connect: connection refused"
    expected: [INC-2026-001]
  - query: "lenteur des rapports mensuels"
    expected: [INC-2026-004]
  - query: "certificat ingress kubernetes"   # aucun résultat attendu
    expected: []
```

```bash
bin/amem eval run eval.yaml -o eval-bge-m3.json
bin/amem eval run eval.yaml --compare eval-bge-m3.json
```

## Variables

| Variable | Défaut |
|---|---|
| `AMEM_DATABASE_URL` | `postgres://amem:amem@127.0.0.1:5433/amem?sslmode=disable` |
| `AMEM_ROOT` | `~/.local/share/amem/projects` |
| `AMEM_PROJECT` | — (résolution par dépôt Git) |
| `AMEM_PRINCIPAL` | détecté (`human:<user>` ou `agent:…`) |
| `AMEM_LISTEN` | `127.0.0.1:7420` (`amem serve`) |
| `AMEM_EMBEDDINGS_PROVIDER` | — (lexical) ; `ollama` ou `openai` |
| `AMEM_EMBEDDINGS_BASE_URL` | `http://localhost:11434` (ollama), `https://api.openai.com/v1` (openai) |
| `AMEM_EMBEDDINGS_MODEL` | `bge-m3` (ollama) ; obligatoire pour openai |
| `AMEM_EMBEDDINGS_API_KEY_ENV` | — (nom de la variable qui contient la clé) |
| `AMEM_EMBEDDINGS_DIMS` | — (dimension demandée au fournisseur OpenAI) |
| `AMEM_EMBEDDINGS_QUERY_PREFIX` / `_DOCUMENT_PREFIX` | — (ex. `query: ` / `passage: ` pour e5) |
| `AMEM_EMBEDDINGS_QUERY_TIMEOUT` | `1s` (au-delà : mode lexical pour cette recherche) |

## Tests

```bash
make test   # unitaires + intégration (base jetable créée sur le PostgreSQL local), avec -race ; tests sémantiques ignorés sans pgvector
```

La même suite tourne dans GitHub Actions ([`.github/workflows/tests.yml`](.github/workflows/tests.yml)) à chaque push sur `main` et sur chaque pull request, avec la construction de l'image Docker.
