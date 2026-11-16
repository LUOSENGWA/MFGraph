-- Actor types. A host platform names every actor by a reference and a type
-- (for example "human" or "worker", [a-z0-9_-]{1,16}), and actors of
-- different types may have the same reference. Embedded in a host platform
-- MFGraph therefore records the type next to each actor reference it
-- writes; in a standalone deployment the types stay NULL.
--   snapshots.author_type, snapshots.on_behalf_of_type: the types of
--     author_ref and on_behalf_of_ref;
--   idempotency_keys.actor_type: the type of actor_ref (a record is only
--     replayed to the same actor; NULL and NULL are the same type);
--   workspaces.owner_type: the type of owner_ref.
-- Each statement adds one nullable column, so a run that stopped between
-- statements can be repeated: the runner skips a column that already
-- exists.
ALTER TABLE snapshots ADD COLUMN author_type VARCHAR(16) NULL;
ALTER TABLE snapshots ADD COLUMN on_behalf_of_type VARCHAR(16) NULL;
ALTER TABLE idempotency_keys ADD COLUMN actor_type VARCHAR(16) NULL;
ALTER TABLE workspaces ADD COLUMN owner_type VARCHAR(16) NULL;
