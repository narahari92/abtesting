-- Conversion attribution looks up a visitor's exposures within a site.
CREATE INDEX exposures_site_visitor_idx ON exposures (site_id, visitor_id);
