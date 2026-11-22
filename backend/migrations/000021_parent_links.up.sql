-- 000021_parent_links: parent–student links (IAM-09, decision 0016). A parent's account is a user linked to a
-- guardian record (users.person_type = 'guardian'); the guardian's student_guardians rows say which children the
-- account sees. The school can stop one link from giving access (custody), without removing the guardian from the
-- child's file.

ALTER TABLE student_guardians ADD COLUMN no_access boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN student_guardians.no_access IS 'True when this guardian''s account must not see this student (for example a custody order). The guardian stays on the student''s file.';

-- One account per person: two accounts linked to the same guardian (or student) would both see the same children.
CREATE UNIQUE INDEX users_tenant_person_uq ON users (tenant_id, person_type, person_id) WHERE person_id IS NOT NULL AND deleted_at IS NULL;
