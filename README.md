# Agent Memory Core (`amem`)

Mémoire durable **par projet** pour agents IA : incidents, décisions, conventions, architecture, procédures et leçons, partagées par tous les agents et développeurs d'un projet — qu'ils soient lancés par un orchestrateur (Multica) ou directement (Claude Code, Codex, autre client MCP).

Spécifications : [générale](docs/00-specification-generale.md) · [fonctionnelle](docs/01-specification-fonctionnelle.md) · [technique](docs/02-specification-technique.md).

## Démarrage

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

## Variables

| Variable | Défaut |
|---|---|
| `AMEM_DATABASE_URL` | `postgres://amem:amem@127.0.0.1:5433/amem?sslmode=disable` |
| `AMEM_ROOT` | `~/.local/share/amem/projects` |
| `AMEM_PROJECT` | — (résolution par dépôt Git) |
| `AMEM_PRINCIPAL` | détecté (`human:<user>` ou `agent:…`) |
| `AMEM_LISTEN` | `127.0.0.1:7420` (`amem serve`) |

## Tests

```bash
make test   # unitaires + intégration (base jetable créée sur le PostgreSQL local), avec -race
```
