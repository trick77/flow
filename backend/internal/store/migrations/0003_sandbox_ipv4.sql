-- Each sandbox's address on the sandbox network (docs/architecture.md, "Sandbox network"), held from its first
-- bring-up until the row is deleted; NULL while it holds none. Written only by sandboxes.SQLAddresses. The unique
-- index makes two rows holding one address impossible whatever races (SQLite allows any number of NULLs).
ALTER TABLE sandboxes ADD COLUMN ipv4 TEXT;
CREATE UNIQUE INDEX sandboxes_ipv4 ON sandboxes (ipv4);
