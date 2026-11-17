-- 20261117192018_experiment_assignment_plan.up.sql — how the issue a draw was made for is done.
--
-- A variant of an experiment may name `plan` (config.Variant.Plan): `owner`, whose issues take the
-- plan their owner gives, `default`, whose issues run the project's default steps and take no plan
-- from their owner, and `owner-alone`. The draw records that word on its row beside the arm
-- (experiment.DrawAndRecord), so an issue keeps the way it is done for its whole life, whatever
-- the project's file says later: the row is written once and never updated
-- (20261111045636_experiment_assignments, the paragraph on the primary key). The workflow reads it
-- off the issue's own rows (experiment.GroupOf). The check keeps the empty word and the three words
-- the loader takes (config.PlanGroup).
--
-- It back-fills nothing. Every row written before it reads '', which is an issue of no group: it
-- runs as an issue with no experiment runs, as it did when it was drawn. A carried or settled row
-- (record.Store.CarryAssignments) names its columns and leaves this one out, so it reads '' too,
-- and only a tree row is carried, which no experiment that sets plan draws (config's readExperiment
-- holds such an experiment to unit issue).
--
-- Rolling back onto a release before this migration needs nothing taken off. That release names
-- the columns of experiment_assignments in each statement it runs — its record.Store's
-- assignmentColumns, PutAssignments and CarryAssignments — so it boots with the column in place,
-- reads no word of it, and writes '' into it at each draw. What the roll back costs: an issue
-- drawn into `default` before the roll back has its plan read by no code while that release runs,
-- so the plan its owner gives is taken and run. Rolling forward again costs the window the other
-- way: an issue that release drew while it ran reads '' afterwards and runs as with no experiment.
alter table experiment_assignments
    add column plan text not null default ''
        check (plan in ('', 'owner', 'default', 'owner-alone'));
