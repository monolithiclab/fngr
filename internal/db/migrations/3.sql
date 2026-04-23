ALTER TABLE events RENAME COLUMN text TO title;
ALTER TABLE events ADD COLUMN body TEXT NOT NULL DEFAULT '';

-- Two-pass split on first '. ' separator. Order matters: populate
-- body first, then truncate title — pass 2 destroys the INSTR
-- landmark. Both sides are TRIMmed to match parse.SplitTitleBody.
-- The no-separator branch is trimmed by the third UPDATE below.
UPDATE events
   SET body = TRIM(SUBSTR(title, INSTR(title, '. ') + 2))
 WHERE INSTR(title, '. ') > 0;

UPDATE events
   SET title = TRIM(SUBSTR(title, 1, INSTR(title, '. ') - 1))
 WHERE INSTR(title, '. ') > 0;

-- For rows with no '. ' separator, the title is the whole input;
-- still trim it so SQL parity with parse.SplitTitleBody holds for
-- legacy data with surrounding whitespace.
UPDATE events
   SET title = TRIM(title)
 WHERE INSTR(title, '. ') = 0;

-- Rebuild events_fts content from new columns + meta key=value tokens.
-- Formula must match internal/parse/parse.go::FTSContent.
DELETE FROM events_fts;
INSERT INTO events_fts(rowid, content)
SELECT e.id,
       TRIM(
         CASE WHEN e.title <> '' THEN e.title || ' ' ELSE '' END ||
         CASE WHEN e.body  <> '' THEN e.body  || ' ' ELSE '' END ||
         COALESCE(GROUP_CONCAT(em.key || '=' || em.value, ' '), '')
       )
  FROM events e
  LEFT JOIN event_meta em ON em.event_id = e.id
 GROUP BY e.id;
