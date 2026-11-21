# Spécification fonctionnelle — Agent Memory Core

**Version :** 0.3
**Date :** 20 novembre 2026
**Référence :** [00 — Spécification générale](00-specification-generale.md)
**Suite :** [02 — Spécification technique](02-specification-technique.md)

Ce document décrit **ce que fait** le système, du point de vue de ses utilisateurs (agents et humains), indépendamment de son implémentation. Chaque exigence porte un identifiant (`EF-`, `RG-`, `ENF-`) référencé par la spécification technique et les tests d'acceptation.

---

## 1. Objet et périmètre

Agent Memory Core (`amem`) offre à des agents IA et à des humains une mémoire de projet partagée, durable et interrogeable : incidents, décisions, conventions, architecture, procédures et leçons apprises. Il permet de :

- **consulter** une carte d'orientation du projet ;
- **rechercher** une connaissance par question, message d'erreur, identifiant ou filtres ;
- **lire** le détail d'une connaissance et son historique ;
- **enregistrer** et **faire évoluer** des connaissances, avec contrôle de concurrence ;
- **déposer** des matériaux bruts (sessions, rapports) dont le système extrait des propositions ;
- **valider** ces propositions (revue humaine) ;
- **administrer** projets, accès, index, sauvegardes et qualité de recherche.

Le périmètre exclu est celui du §3 de la spécification générale.

## 2. Acteurs

| Acteur | Description | Rôle par défaut |
|---|---|---|
| **Agent** | Agent IA (Claude Code, Codex, Multica, Omnara…) agissant via MCP, API ou CLI, identifié par son propre jeton | `contributor` |
| **Développeur** | Humain travaillant sur le projet, via CLI ou API | `contributor` |
| **Curateur** | Humain responsable de la qualité de la mémoire : valide, conteste, rend obsolète | `curator` |
| **Administrateur** | Gère projets, principaux, jetons, sauvegardes, index | `admin` |
| **Orchestrateur** | Système qui lance des agents et peut ingérer leurs résultats (ex. Multica) | `contributor` |
| **Système** | Travaux asynchrones internes (indexation, extraction, génération de cartes, purge) | interne |

## 3. Glossaire

| Terme | Définition |
|---|---|
| Projet | Unité de mémoire : un produit logiciel, éventuellement réparti sur plusieurs dépôts ; identifié par un slug (`project_id`). Toute connaissance appartient à exactement un projet |
| Résolution du projet | Détermination du projet concerné par un appel (argument, configuration, dépôt Git courant, référence externe) |
| Orchestrateur | Outil qui lance des agents pour traiter des tickets (Multica). À distinguer d'un **agent direct** lancé par un développeur (Claude Code, Codex) |
| Workspace Multica | Espace de Multica regroupant plusieurs projets Multica ; ce n'est **pas** un projet amem |
| Connaissance | Unité durable, versionnée, typée ; un fichier Markdown à en-tête YAML |
| Clé | Identifiant lisible d'une connaissance, ex. `INC-2026-014` |
| Carte (L0) | Document compact d'orientation du projet |
| Entrée L1 | Résumé d'une connaissance renvoyé par la recherche ou un index |
| Détail (L2) | Contenu complet d'une connaissance |
| Matériau brut | Texte déposé pour extraction (transcription, rapport, note) |
| Proposition | Connaissance au statut `proposed` |
| Principal | Identité authentifiée, humaine ou agent |
| Source | Référence typée justifiant une connaissance (commit, fichier, ticket, session, URL, log) |

## 4. Concepts métier

### 4.1 Types de connaissance

| Type | Préfixe | Usage | Sections L2 attendues |
|---|---|---|---|
| `incident` | INC | Problème rencontré | Symptômes, Contexte, Diagnostic, Cause, Solution, Prévention, Références |
| `decision` | DEC | Choix structurant (type ADR) | Contexte, Options, Décision, Conséquences, Références |
| `architecture` | ARC | Description d'une structure ou d'un flux | Vue d'ensemble, Éléments, Flux, Contraintes |
| `component` | CMP | Fiche d'un module | Rôle, Interfaces, Dépendances, Pièges |
| `convention` | CNV | Règle à respecter | Règle, Justification, Exemples, Exceptions |
| `lesson` | LSN | Enseignement tiré | Constat, Enseignement, Application |
| `procedure` | PRC | Mode opératoire | Prérequis, Étapes, Vérification, Retour arrière |
| `note` | NOTE | Connaissance non classée | libre |

Les sections sont recommandées (gabarits fournis par `amem record`), pas imposées.

### 4.2 Statut de validité

| Statut | Signification | Visible par défaut en recherche |
|---|---|---|
| `proposed` | Proposée, non validée | Oui, marquée comme telle |
| `confirmed` | Validée | Oui |
| `disputed` | Contestée (motif obligatoire) | Oui, avec avertissement |
| `obsolete` | N'est plus applicable | Non |
| `superseded` | Remplacée par une autre connaissance | Non (la remplaçante est renvoyée) |
| `rejected` | Proposition refusée en revue | Non |

### 4.3 Autres qualificatifs
- **Confiance** : `low` / `medium` / `high`, avec justification optionnelle. Indépendante du statut.
- **Résolution** (incidents) : `open`, `mitigated`, `resolved`, `wont_fix`.
- **Gravité** (incidents) : `low`, `medium`, `high`, `critical`.
- **Classification** : `internal` (défaut) ou `restricted`.
- **Origine** : `manual`, `agent`, `extracted`, `imported`.
- **Nature d'une affirmation** (`claims`) : `observed`, `hypothesis`, `proposal`, `conclusion`.

### 4.4 Relations
`relates_to`, `caused_by`, `fixes`, `duplicates`, `contradicts`, `depends_on`, ainsi que `supersedes` (champ dédié). Les relations sont orientées ; la consultation renvoie les deux sens.

## 5. Cas d'utilisation

Format : acteur principal · préconditions · scénario nominal · variantes/erreurs · résultat.

### UC-01 — Initialiser un projet
- **Acteur** : Administrateur ou Développeur (mode local).
- **Préconditions** : aucun projet de même slug.
- **Nominal** : 1) `amem init <slug> [--name …] [--repo <url> …] [--path <dir>]` ; 2) le système crée l'arborescence, `project.yaml` (nom, description, dépôts liés — plusieurs dépôts possibles —, vocabulaire de thèmes vide, exclusions par défaut) ; 3) enregistre le projet ; 4) donne le rôle `admin` à l'initiateur ; 5) génère une carte L0 initiale.
- **Variantes** : répertoire existant contenant des connaissances → proposé à l'import (UC-15). Slug déjà pris → erreur `project_exists`. Dépôt déjà rattaché à un autre projet → erreur `repository_taken` (un dépôt n'appartient qu'à un projet).
- **Après coup** : `amem project add-repo <slug> <url>`, `amem project link <slug> multica:<workspace>/<projet>` ajoutent un dépôt ou une référence externe.
- **Résultat** : projet utilisable, carte disponible.

### UC-02 — Consulter la carte du projet (L0)
- **Acteur** : Agent (typiquement au démarrage), Développeur.
- **Nominal** : 1) l'acteur demande la carte ; 2) le système renvoie la carte en vigueur, sa date de génération et un indicateur de fraîcheur.
- **Variantes** : carte périmée (connaissances modifiées depuis) → renvoyée avec `stale: true` et régénération planifiée. Projet vide → carte minimale indiquant comment enregistrer une première connaissance.
- **Résultat** : ≤ 2 000 tokens d'orientation.

### UC-03 — Rechercher une connaissance
- **Acteur** : Agent, Développeur.
- **Entrées** : requête texte (optionnelle si filtres), contexte (fichiers, composant, message d'erreur), filtres (types, statuts, thèmes, composants, technologies, période, gravité, résolution), `limit` (défaut 5, max 20), `max_tokens`.
- **Nominal** : 1) le système vérifie les droits ; 2) détecte les éléments exacts (clés, signatures d'erreur, symboles) ; 3) exécute recherche exacte, lexicale et sémantique ; 4) fusionne, classe, déduplique ; 5) renvoie des entrées L1 avec, pour chacune : clé, titre, abstract, overview, type, statut, confiance, résolution, composants, sources, date, raisons de correspondance (`exact`, `lexical`, `semantic`, `filter`).
- **Variantes** :
  - aucune connaissance au-dessus du seuil → `no_relevant_result: true` et liste vide (ou éventuels résultats faibles marqués comme tels si `include_weak`) ;
  - service d'embeddings indisponible → résultats lexicaux, `degraded: ["semantic"]` ;
  - budget dépassé → `overview` tronqués puis entrées retirées, `truncated: true` ;
  - requête par clé exacte → la connaissance est renvoyée en tête quel que soit son statut.
- **Résultat** : en un appel, l'agent dispose de résumés sourcés exploitables.

### UC-04 — Lire le détail (L2)
- **Acteur** : Agent, Développeur.
- **Nominal** : 1) lecture par clé ou ULID ; 2) le système renvoie en-tête complet, corps Markdown, version, ETag, relations, et la remplaçante si `superseded`.
- **Variantes** : version donnée (`version=n`) → version historique ; document > 8 000 tokens → pagination par section ; `restricted` sans droit → `not_found` (pas de divulgation d'existence) ; connaissance supprimée → `gone` pour les curateurs, `not_found` sinon.

### UC-05 — Enregistrer une connaissance
- **Acteur** : Agent, Développeur.
- **Entrées** : type, titre, abstract, overview, corps, métadonnées, sources, relations, classification, clé d'idempotence (optionnelle).
- **Nominal** : 1) validation du schéma ; 2) détection de secrets ; 3) recherche de quasi-doublons ; 4) attribution de la clé ; 5) écriture du document, version 1, statut initial (RG-03) ; 6) audit ; 7) indexation planifiée ; 8) renvoi de la clé, de l'ETag et des éventuels doublons potentiels.
- **Variantes** : secret détecté → rejet `secret_detected` avec position et type, sans écho du secret ; quasi-doublon fort → création acceptée mais avertissement `possible_duplicates` (ou refus si `on_duplicate=reject`) ; même clé d'idempotence déjà traitée → renvoi du résultat initial, aucune création.

### UC-06 — Modifier une connaissance
- **Acteur** : Agent, Développeur, Curateur.
- **Préconditions** : l'acteur connaît la version courante (ETag).
- **Nominal** : 1) envoi d'un correctif (champs modifiés et/ou nouveau corps) + version attendue ; 2) contrôle des droits (RG-07) ; 3) contrôle de version ; 4) archivage de la version précédente dans l'historique ; 5) écriture de la nouvelle version ; 6) audit et réindexation.
- **Variantes** : version différente → `version_conflict` avec version courante et différences de champs ; aucune modification réelle → aucune nouvelle version ; tentative de changer `type` ou `key` → refus `immutable_field` ; changement de statut par ce biais → refus, utiliser UC-07.

### UC-07 — Changer le statut d'une connaissance
- **Acteur** : Curateur (toutes transitions), Contributeur (transitions limitées, RG-05).
- **Nominal** : 1) demande de transition avec motif ; 2) vérification de la matrice de transitions ; 3) nouvelle version ; 4) audit ; 5) mise à jour de la carte si nécessaire.
- **Variante « remplacer »** : l'acteur enregistre (UC-05) une nouvelle connaissance avec `supersedes: <clé>` ; à sa confirmation, l'ancienne passe automatiquement `superseded`.

### UC-08 — Explorer les connaissances liées
- **Acteur** : Agent, Développeur.
- **Nominal** : à partir d'une clé, renvoi des entrées L1 liées, groupées par type de relation et par sens, plus les connaissances partageant le plus de composants (limitées à 10).

### UC-09 — Déposer un matériau brut pour extraction
- **Acteur** : Agent (fin de tâche), Orchestrateur, Développeur.
- **Entrées** : texte ou fichier (≤ 2 Mo), type de matériau (`session`, `task_report`, `incident_log`, `note`), contexte (ticket, commits, composant), indice de types attendus.
- **Nominal** : 1) masquage des secrets ; 2) application des exclusions ; 3) stockage dans `sessions/` ; 4) planification de l'extraction ; 5) réponse immédiate avec un identifiant de travail.
- **Suite asynchrone** : extraction → propositions (`proposed`, origine `extracted`), chacune liée à la session source ; quasi-doublons transformés en propositions de mise à jour de la connaissance existante.
- **Variantes** : aucun fournisseur LLM configuré → matériau stocké, travail en attente `needs_llm`, signalé par `amem doctor`.

### UC-10 — Revoir les propositions
- **Acteur** : Curateur.
- **Nominal** : 1) `amem review` liste les propositions par ancienneté et gravité ; 2) pour chacune, affichage du résumé, des affirmations qualifiées, des sources et des doublons potentiels ; 3) le curateur confirme, modifie puis confirme, fusionne dans une connaissance existante, ou rejette avec motif.
- **Résultat** : statut mis à jour, audit tracé.

### UC-11 — Donner un retour sur un résultat
- **Acteur** : Agent, Développeur.
- **Entrées** : identifiant de recherche, clé, verdict (`useful`, `not_useful`, `wrong`, `outdated`), commentaire.
- **Nominal** : enregistrement du retour ; `wrong`/`outdated` ajoutent la connaissance à la file de revue ; les retours alimentent le jeu d'évaluation candidat.

### UC-12 — Gérer les principaux et les accès
- **Acteur** : Administrateur.
- **Nominal** : créer un principal (humain/agent), émettre/révoquer des jetons (affichés une seule fois, avec expiration), attribuer un rôle par projet, accorder l'accès à une connaissance `restricted`.

### UC-13 — Reconstruire les index
- **Acteur** : Administrateur, Système (réconciliation).
- **Nominal** : `amem index rebuild [--project P] [--embeddings]` recrée les données dérivées depuis les fichiers canoniques, sans interrompre la lecture (bascule à la fin). `amem index status` indique les documents en retard ou divergents.

### UC-14 — Sauvegarder et restaurer
- **Acteur** : Administrateur.
- **Nominal** : `amem backup` produit une archive horodatée (fichiers canoniques + données opérationnelles non reconstructibles) ; `amem restore` la réapplique puis reconstruit les index. Une restauration vers un projet existant non vide exige `--force`.

### UC-15 — Importer et exporter
- **Acteur** : Administrateur, Développeur.
- **Nominal** : import d'un répertoire de fichiers Markdown conformes (validation, attribution de clés manquantes, origine `imported`, statut conservé si `confirmed` et importateur curateur, sinon `proposed`) ; export d'un projet en archive de fichiers canoniques.

### UC-16 — Évaluer la qualité de recherche
- **Acteur** : Administrateur, Développeur.
- **Nominal** : `amem eval run <jeu.yaml>` exécute les requêtes du jeu, compare aux clés attendues, produit Recall@k, Precision@k, MRR, nDCG@10, latences, et compare au dernier rapport.

### UC-17 — Brancher un agent direct (Claude Code, Codex, autre)
- **Acteur** : Développeur.
- **Nominal** : 1) dans le dépôt, `amem adapter install claude-code` (ou `codex`) écrit la configuration MCP du produit (`.mcp.json` du dépôt pour Claude Code, `~/.codex/config.toml` pour Codex) et un court bloc d'instructions (`CLAUDE.md` / `AGENTS.md`) ; 2) `amem adapter check` démarre le serveur MCP comme le ferait l'agent, vérifie la résolution du projet et appelle `memory_overview`.
- **Variantes** : dépôt non rattaché à un projet → proposition de `amem project add-repo` ; fichier d'instructions existant → bloc ajouté entre balises `<!-- amem:start -->` / `<!-- amem:end -->`, remplacé à la réinstallation, jamais dupliqué.

### UC-18 — Brancher un orchestrateur (Multica)
- **Acteur** : Administrateur, Développeur.
- **Préconditions** : projet amem créé et rattaché aux dépôts que traitent les agents Multica.
- **Nominal** : 1) `amem adapter install multica --agent <nom> [--mode stdio|http]` crée le principal `agent:multica/<nom>`, son jeton (mode HTTP), et produit le JSON `mcp_config` à coller dans la configuration de l'agent Multica (ou à déclarer comme serveur MCP du workspace Multica lié aux agents) ; 2) il produit le bloc d'instructions à ajouter aux instructions de l'agent ; 3) lors d'une tâche, le démon Multica lance l'agent avec ce `mcp_config` ; le serveur amem résout le projet depuis les dépôts de la tâche ; 4) l'agent consulte la mémoire puis enregistre ce qu'il a appris en citant le ticket (`ticket:multica/<id>`).
- **Variantes** : mode stdio → `amem` doit être installé sur chaque machine qui exécute un démon Multica et joindre la base ; mode HTTP → seul un serveur amem est requis, le jeton voyage dans l'en-tête ; tâche sans dépôt → le projet vient de l'argument `project`, de l'en-tête `X-Amem-Project` fixé pour l'agent, ou de la référence externe Multica.
- **Résultat** : les mêmes outils et les mêmes données que pour un agent direct.

### UC-19 — Résoudre le projet d'un appel
- **Acteur** : Système (à chaque appel), Agent (`memory_projects`).
- **Nominal** : le système applique l'ordre de résolution (spécification générale §4.5) : argument `project` › configuration du processus ou en-tête › dépôt Git du répertoire courant et de ses sous-répertoires immédiats › référence externe d'orchestrateur › projet unique accessible.
- **Variantes** : aucun projet trouvé → `project_required` avec la liste des projets accessibles ; plusieurs dépôts de projets différents dans le répertoire de la tâche → `project_ambiguous` avec les candidats ; projet trouvé mais non autorisé → `not_found` ; argument explicite contredisant le dépôt → l'argument l'emporte, `warnings: ["project_mismatch"]`.
- **Résultat** : `memory_projects` renvoie les projets accessibles, le projet résolu et la règle qui l'a désigné.

## 6. Exigences fonctionnelles

### 6.1 Projets
- **EF-001** Créer, lister, décrire et archiver un projet.
- **EF-002** Associer un projet à zéro ou plusieurs dépôts Git (URL + chemin local optionnel).
- **EF-003** Maintenir dans `project.yaml` un vocabulaire de thèmes avec alias (`paiement` ⇐ `payment`, `billing`).
- **EF-004** Toute opération est rattachée à exactement un projet ; aucune recherche multi-projets implicite.
- **EF-005** Rattacher plusieurs dépôts à un projet ; refuser qu'un dépôt appartienne à deux projets. Les URL distantes sont normalisées (`git@host:a/b.git`, `https://host/a/b` → `host/a/b`).
- **EF-006** Résoudre le projet de chaque appel selon UC-19 et indiquer dans chaque réponse le projet utilisé.
- **EF-007** Associer un projet à des références externes (ex. projet Multica) utilisables pour la résolution.
- **EF-008** Fournir l'outil `memory_projects` (projets accessibles, projet résolu, règle appliquée).

### 6.2 Connaissances
- **EF-010** Créer une connaissance conforme au modèle (spécification générale §6).
- **EF-011** Attribuer une clé `<PRÉFIXE>-<AAAA>-<NNN>` séquentielle par projet, type et année ; ne jamais réutiliser une clé.
- **EF-012** Normaliser thèmes (minuscules, alias résolus), composants et technologies.
- **EF-013** Lire une connaissance par clé ou ULID, en version courante ou historique.
- **EF-014** Modifier une connaissance avec version attendue obligatoire ; refuser sinon.
- **EF-015** Conserver chaque version antérieure, consultable et comparable (`amem read --diff v2..v3`).
- **EF-016** Supprimer logiquement (tombstone) ; purge physique réservée à l'administrateur, auditée.
- **EF-017** Gérer les relations typées et `supersedes`, avec vérification que la cible existe dans le même projet.
- **EF-018** Fournir des gabarits de corps par type.
- **EF-019** Accepter une clé d'idempotence sur toute création ; durée de mémorisation 24 h.

### 6.3 Navigation
- **EF-020** Générer la carte L0 : description, composants majeurs, technologies, conventions `confirmed`, thèmes avec effectifs, incidents `open`/`mitigated` de gravité `high`/`critical`, décisions `confirmed` récentes ou marquées structurantes, pointeurs d'index, date.
- **EF-021** Respecter le budget de la carte (≤ 1 500 tokens visés, 2 000 max) en tronquant les listes par priorité.
- **EF-022** Régénérer la carte après modification (délai ≤ 5 min, regroupement des modifications).
- **EF-023** Générer des index L1 lisibles par thème, composant et technologie dans `indexes/`.

### 6.4 Recherche
- **EF-030** Recherche par texte libre, contexte et filtres combinables.
- **EF-031** Priorité aux correspondances exactes de clé, mot-clé déclaré ou signature d'erreur.
- **EF-032** Recherche lexicale tolérante (racines, accents, fautes de frappe sur identifiants).
- **EF-033** Recherche sémantique multilingue (au minimum français et anglais).
- **EF-034** Fusion et classement selon des poids configurables par projet.
- **EF-035** Exclusion par défaut de `obsolete`, `superseded`, `rejected` ; inclusion par filtre.
- **EF-036** Un seul résultat par connaissance ; repli des chaînes de remplacement vers la version courante.
- **EF-037** Renvoi explicite « aucune connaissance suffisamment pertinente ».
- **EF-038** Budget de tokens respecté et troncature signalée.
- **EF-039** Chaque résultat indique ses raisons de correspondance et ses sources.
- **EF-040** Chaque recherche reçoit un identifiant permettant le retour (UC-11).
- **EF-041** Mode dégradé lexical si le fournisseur d'embeddings est indisponible, signalé.

### 6.5 Extraction et revue
- **EF-050** Ingérer un matériau brut (texte ≤ 2 Mo) et répondre sans attendre l'extraction.
- **EF-051** Extraire des propositions typées avec abstract, overview, affirmations qualifiées et sources.
- **EF-052** Ne jamais créer une connaissance extraite à un autre statut que `proposed`.
- **EF-053** Détecter les quasi-doublons et produire une proposition de mise à jour au lieu d'une création.
- **EF-054** Détecter les contradictions potentielles avec des connaissances `confirmed` et créer une relation `contradicts` à revoir.
- **EF-055** Fournir une file de revue filtrable (type, ancienneté, origine, gravité).
- **EF-056** Permettre de confirmer, modifier, fusionner ou rejeter une proposition.
- **EF-057** Purger les matériaux bruts après la durée de rétention ; marquer « source expirée » les références correspondantes.

### 6.6 Gouvernance et sécurité
- **EF-060** Authentifier chaque appel (sauf mode local stdio : principal local implicite).
- **EF-061** Appliquer la matrice de droits (§8).
- **EF-062** Masquer l'existence des connaissances `restricted` aux non-autorisés.
- **EF-063** Détecter les secrets (clés d'API, jetons, mots de passe, clés privées, chaînes à forte entropie) à l'écriture et à l'ingestion.
- **EF-064** Appliquer exclusions de chemins et motifs configurés dans `project.yaml`.
- **EF-065** Journaliser : créations, modifications, transitions, suppressions, lectures `restricted`, gestion des accès, restaurations.
- **EF-066** Encapsuler tout contenu restitué comme donnée non fiable et signaler les motifs d'instruction suspects (`suspicious_content: true`).
- **EF-067** Consulter le journal d'audit filtré par projet, principal, clé, période.

### 6.7 Exploitation
- **EF-070** Reconstruire les index sans interruption de lecture.
- **EF-071** Réconcilier fichiers et index au démarrage et périodiquement.
- **EF-072** Sauvegarder et restaurer.
- **EF-073** Diagnostiquer l'installation (`amem doctor` : base, extensions, fournisseurs, droits fichiers, travaux bloqués, index périmés).
- **EF-074** Évaluer la qualité de recherche sur un jeu de tests.
- **EF-075** Installer et vérifier une intégration, pour les deux familles : orchestrateur (Multica) et agent direct (Claude Code, Codex).

## 7. Règles de gestion

- **RG-01 — Clé** : préfixe selon le type, année de création, compteur sur 3 chiffres minimum (s'étend au-delà de 999). Unique par projet, jamais réattribuée, même après purge.
- **RG-02 — Immuabilité** : `id`, `key`, `type`, `project_id`, `created_at`, `created_by` ne changent jamais. Changer de type = nouvelle connaissance qui `supersedes` l'ancienne.
- **RG-03 — Statut initial** : `proposed` pour toute création par un `contributor` ou par extraction ; un `curator` peut créer directement en `confirmed` s'il fournit au moins une source.
- **RG-04 — Confirmation** : `confirmed` exige au moins une `source_ref`. Un agent ne peut pas confirmer une connaissance qu'il a créée, sauf règle de preuve automatique activée dans `project.yaml` (spécification générale §17-Q4).
- **RG-05 — Transitions** :

  | De \ Vers | confirmed | disputed | obsolete | superseded | rejected |
  |---|---|---|---|---|---|
  | proposed | curator | contributor | curator | — | curator |
  | confirmed | — | contributor | curator | auto | — |
  | disputed | curator | — | curator | auto | curator |
  | obsolete | curator | — | — | — | — |
  | superseded | — | — | — | — | — |
  | rejected | — | — | — | — | — |

  Toute transition exige un motif ; `disputed` exige un motif d'au moins 20 caractères.
- **RG-06 — Remplacement** : quand une connaissance portant `supersedes: X` passe `confirmed`, X passe `superseded` automatiquement. Les cycles de remplacement sont refusés.
- **RG-07 — Modification** : un `contributor` peut modifier les connaissances `proposed` ou `disputed` et ajouter sources, mots-clés et relations à une `confirmed` ; modifier abstract, overview ou corps d'une `confirmed` la repasse en `proposed` sauf pour un `curator`.
- **RG-08 — Concurrence** : toute modification porte la version attendue ; en cas d'écart, rien n'est écrit.
- **RG-09 — Doublons** : similarité ≥ seuil fort (défaut 0,92) avec une connaissance non obsolète → avertissement à la création manuelle, proposition de mise à jour pour l'extraction.
- **RG-10 — Secrets** : écriture structurée contenant un secret → rejet. Ingestion → masquage `[REDACTED:<type>]` et poursuite.
- **RG-11 — Restricted** : visible et lisible uniquement par `curator`, `admin` ou principal explicitement autorisé ; jamais incluse dans la carte L0 ni dans les index `indexes/` partagés.
- **RG-12 — Suppression** : logique par défaut (`curator`) ; la clé reste réservée ; les relations entrantes sont marquées cible supprimée. Purge physique : `admin` uniquement, après confirmation, auditée, historique inclus.
- **RG-13 — Rétention** : matériaux bruts 30 jours (configurable 0–365) ; connaissances et historique sans limite ; audit 1 an minimum.
- **RG-14 — Fraîcheur** : la fraîcheur influence le classement par décroissance douce (demi-vie configurable, défaut 365 jours) et ne peut à elle seule faire passer un résultat devant une correspondance exacte.
- **RG-15 — Incidents** : `resolution` et `severity` obligatoires pour le type `incident` ; interdits pour les autres types.
- **RG-16 — Taille** : abstract ≤ 300 caractères, overview ≤ 1 200, titre ≤ 120, corps ≤ 200 000 caractères.

## 8. Matrice des droits

| Opération | reader | contributor | curator | admin |
|---|---|---|---|---|
| Carte, recherche, lecture, relations, historique (`internal`) | ✓ | ✓ | ✓ | ✓ |
| Lecture `restricted` | octroi | octroi | ✓ | ✓ |
| Retour (feedback) | ✓ | ✓ | ✓ | ✓ |
| Créer (→ `proposed`) | | ✓ | ✓ | ✓ |
| Créer directement `confirmed` | | | ✓ | ✓ |
| Modifier (RG-07) | | partiel | ✓ | ✓ |
| Transitions (RG-05) | | partiel | ✓ | ✓ |
| Ingérer un matériau brut | | ✓ | ✓ | ✓ |
| Revue des propositions | | | ✓ | ✓ |
| Suppression logique | | | ✓ | ✓ |
| Purge, sauvegarde, restauration, réindexation | | | | ✓ |
| Principaux, jetons, rôles | | | | ✓ |
| Consulter l'audit | | | ✓ (son projet) | ✓ |

## 9. Exigences non fonctionnelles

- **ENF-01 Performance** : recherche p95 < 500 ms, lecture L2 p95 < 100 ms, carte p95 < 50 ms, sur le projet de référence (10 000 connaissances, 100 000 tranches), embeddings locaux.
- **ENF-02 Fraîcheur des index** : une modification est trouvable en lexical en < 5 s, en sémantique en < 60 s (hors indisponibilité du fournisseur).
- **ENF-03 Disponibilité de l'écriture** : l'indisponibilité d'un fournisseur d'embeddings ou de LLM n'empêche jamais lecture, écriture ni recherche lexicale.
- **ENF-04 Durabilité** : aucune écriture acquittée n'est perdue après arrêt brutal.
- **ENF-05 Isolation** : zéro fuite inter-projet, vérifiée par tests à chaque version.
- **ENF-06 Portabilité** : Linux et macOS (amd64, arm64) ; Windows best effort.
- **ENF-07 Installation** : mode local opérationnel en < 5 minutes avec Docker.
- **ENF-08 Lisibilité** : un humain peut lire et modifier les fichiers canoniques avec un éditeur de texte ; les modifications externes sont détectées et réindexées.
- **ENF-09 Compatibilité** : API et outils MCP versionnés ; pas de rupture au sein d'une version majeure.
- **ENF-10 Langue** : contenu multilingue ; messages CLI en anglais, documentation en français et anglais.

## 10. Critères d'acceptation

```gherkin
Fonctionnalité: Recherche

  Scénario: retrouver un incident par message d'erreur exact
    Étant donné l'incident confirmé "INC-2026-014" ayant pour mot-clé "ECONNREFUSED 127.0.0.1:6379"
    Quand un agent recherche "Error: connect ECONNREFUSED 127.0.0.1:6379 at TCPConnectWrap"
    Alors "INC-2026-014" est le premier résultat
    Et ses raisons de correspondance contiennent "exact"
    Et la réponse tient dans 1 500 tokens

  Scénario: formulation différente
    Étant donné l'incident "INC-2026-014" intitulé "Redis inaccessible au démarrage du worker"
    Quand un agent recherche "le cache refuse les connexions quand la file démarre"
    Alors "INC-2026-014" figure dans les 3 premiers résultats

  Scénario: rien de pertinent
    Quand un agent recherche "configuration du lecteur de disquettes"
    Alors la réponse indique no_relevant_result

  Scénario: connaissance remplacée
    Étant donné "DEC-2026-003" remplacée par "DEC-2026-011" confirmée
    Quand un agent recherche un sujet couvert par les deux
    Alors seule "DEC-2026-011" est renvoyée

  Scénario: fournisseur d'embeddings indisponible
    Étant donné que le fournisseur d'embeddings ne répond pas
    Quand un agent recherche "Redis"
    Alors des résultats lexicaux sont renvoyés
    Et la réponse indique degraded ["semantic"]

Fonctionnalité: Concurrence

  Scénario: deux agents modifient la même version
    Étant donné "INC-2026-014" en version 3
    Quand l'agent A et l'agent B envoient chacun une modification attendant la version 3
    Alors exactement une modification réussit et produit la version 4
    Et l'autre reçoit version_conflict avec la version courante 4
    Et aucun contenu n'est perdu

Fonctionnalité: Projets et intégrations

  Scénario: même mémoire depuis Multica et depuis Claude Code
    Étant donné le projet "mra" rattaché au dépôt "github.com/acme/mra-api"
    Et un agent Multica "dev-senior" configuré avec le mcp_config produit par amem
    Quand Multica lance "dev-senior" sur un ticket dont la tâche contient le dépôt "mra-api"
    Et que l'agent enregistre l'incident "INC-2026-020"
    Alors un développeur qui lance Claude Code dans un clone de "mra-api" retrouve "INC-2026-020" par memory_search
    Et un Codex lancé dans ce même clone le retrouve aussi

  Scénario: résolution par dépôt
    Étant donné un agent lancé dans un clone de "github.com/acme/mra-api"
    Quand il appelle memory_projects
    Alors le projet résolu est "mra" par la règle "git_remote"

  Scénario: aucun projet
    Étant donné un agent ayant accès aux projets "mra" et "shop", lancé hors de tout dépôt rattaché
    Quand il appelle memory_search sans argument project
    Alors il reçoit project_required avec la liste ["mra", "shop"]

Fonctionnalité: Gouvernance

  Scénario: isolation
    Étant donné un agent n'ayant accès qu'au projet "alpha"
    Quand il recherche, lit ou référence une connaissance de "beta" par clé ou ULID
    Alors il obtient not_found

  Scénario: extraction jamais confirmée
    Quand une session est ingérée et extraite
    Alors toutes les connaissances créées ont le statut "proposed" et l'origine "extracted"

  Scénario: secret à l'écriture
    Quand un agent enregistre une connaissance contenant "AKIAIOSFODNN7EXAMPLE"
    Alors l'écriture est refusée avec secret_detected
    Et le secret n'apparaît ni dans la réponse ni dans les journaux

  Scénario: auto-confirmation interdite
    Étant donné une connaissance créée par l'agent A
    Quand l'agent A demande la transition vers "confirmed"
    Alors la transition est refusée avec forbidden
```

## 11. Périmètre par livraison

| Livraison | Exigences couvertes |
|---|---|
| Phase 1 | EF-001–004, EF-010–019, EF-060–061, EF-065, EF-070–071, RG-01–08, RG-12, RG-15–16 |
| Phase 2 | EF-005–008, EF-020–023, EF-030–032, EF-035–040, EF-063–064, EF-066, EF-075 (Multica, Claude Code, Codex), UC-17–19, MCP |
| Phase 3 | EF-033–034, EF-041, EF-074, RG-14 |
| Phase 4 | EF-050–057, RG-09–10 (ingestion), RG-13 |
| Phase 5 | EF-075 (Omnara, autres), EF-007 (résolution par référence externe Multica via son API) |
| Phase 6 | EF-062, EF-067, EF-072–073, RG-11, mode local embarqué |
