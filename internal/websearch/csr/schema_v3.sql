-- Schema version 3: HTTP validators per route, so a recheck can ask the
-- server whether a page (typically a large PDF report) changed instead of
-- downloading it again.

ALTER TABLE company_pages ADD COLUMN etag TEXT;
ALTER TABLE company_pages ADD COLUMN last_modified TEXT;
