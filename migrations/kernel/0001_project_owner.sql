-- Owner of a project in a standalone deployment: the actor reference of
-- the caller that created it (see 0001_workspaces.sql of the module schema
-- for actor references), with an optional principal ULID. The owner is the
-- only actor that can see the project and its workspaces until roles
-- exist. Projects created before this migration have NULL and are visible
-- to nobody. Embedded in a host platform, the host owns its projects table
-- and the kernel migrations are not executed.
-- Each statement adds one column or one index, so a run that stopped
-- between statements can be repeated: the runner skips a column or an
-- index that already exists.
ALTER TABLE projects ADD COLUMN owner_ref VARBINARY(255) NULL;
ALTER TABLE projects ADD COLUMN owner_principal_uid CHAR(26) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL;
ALTER TABLE projects ADD KEY idx_projects_owner (owner_ref, status);
