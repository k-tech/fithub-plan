-- Branch protection is not part of FitHub's merge or push policy.
-- Keep default_branch and ref_decisions (created in earlier migrations), but
-- remove per-repository protection rules and their required-check settings.
DROP TABLE IF EXISTS branch_protections;
