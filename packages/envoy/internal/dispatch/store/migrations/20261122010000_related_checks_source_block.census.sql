-- An ordinary, non-unique index refuses and rewrites no row. Its concurrent build does not hold
-- off ordinary writes while it scans related_checks, whatever the table's size.
select 0;
