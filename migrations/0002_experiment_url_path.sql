-- Each experiment runs on exactly one page of the site, identified by its
-- URL path. Existing experiments default to the site root.
ALTER TABLE experiments ADD COLUMN url_path text NOT NULL DEFAULT '/';
