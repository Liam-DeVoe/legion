-- The rows this migration cannot place: a delivery_settings row whose deploy_repo is the default
-- repository of no project or of more than one. Any such row refuses the deploy, so the person
-- sets that project's default repository first. Production held no delivery_settings row on
-- 2026-11-23 (GET /api/v1/settings/delivery answered null).
select count(*) from public.delivery_settings d
where (select count(*) from public.projects p where p.default_repo = lower(d.deploy_repo)) <> 1;
