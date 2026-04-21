# TABLE_SETUP

## Purpose

`prtags` keeps its production schema under explicit SQL migrations.

Go model definitions exist so the application, tests, and query code have one
authoritative view of table names, columns, and relationships. They do **not**
replace migrations, and they must never be allowed to mutate the production
schema implicitly.

This document covers `prtags` application tables. Migration bookkeeping tables
such as `schema_migrations`, and third-party tables owned by River, are managed
separately and are not part of the GORM model registry.

## Rules

1. Every persisted table must have a concrete Go model in `internal/database/`.
2. Every persisted model must define an explicit `TableName()` method.
3. Production and normal runtime code must use SQL migrations only.
4. `database.AutoMigrate` must stay disabled in runtime code.
5. SQLite tests must use `database.ApplyTestSchema(db)` when they need a local
   schema bootstrap.

## Current layout

- Model definitions live in `internal/database/`.
- Explicit table names live in `internal/database/table_names.go`.
- Test schema bootstrap lives in `internal/database/schema.go`.
- Production schema history lives in `migrations/`.

## Why indexes still use migrations

Table definitions and migrations serve different purposes.

- Go model definitions describe the shape the code expects.
- SQL migrations describe how a live database is changed over time.

Indexes, constraints, column changes, data backfills, and table rewrites must
go through migrations so production keeps an ordered, reviewable schema history.

## How to add a new table

1. Add a persisted model under `internal/database/`.
2. Add an explicit `TableName()` method for it.
3. Add the model to `schemaModels()` in `internal/database/schema.go`.
4. Add a SQL migration that creates the table and any indexes or constraints.
5. If tests need the table, use `database.ApplyTestSchema(db)` rather than
   adding new runtime `AutoMigrate` behavior.
6. Add or update tests that prove the new schema is used correctly.

## How to change an existing table

1. Update the Go model so application code reflects the intended shape.
2. Add a SQL migration for the real schema change.
3. If the change affects SQLite-backed tests, make sure `ApplyTestSchema` still
   creates a compatible local schema.
4. Run the relevant test suites and `git diff --check`.

## Anti-patterns

- Do not call `db.AutoMigrate(...)` from runtime paths.
- Do not rely on implicit GORM pluralization for production table names.
- Do not add indexes by changing only model tags and hoping runtime schema
  mutation will pick them up.
- Do not add a table in Go without also adding a migration.
