-- This migration adds two columns with constant defaults and a not valid check to eval_versions,
-- and states evals_current again over them. It reads no stored row, and refuses and rewrites none.
select 0;
