-- Take back the grants this migration added (nothing else), then drop everything it created.
DELETE FROM role_permissions rp
USING roles r
WHERE rp.role_id = r.id AND rp.tenant_id = r.tenant_id AND NOT r.is_system
  AND ((r.template_code = 'teacher' AND rp.permission_code IN ('stu.student_electives.create', 'stu.student_electives.edit', 'stu.student_elective_approvals.create'))
    OR (r.template_code = 'principal' AND rp.permission_code IN ('stu.student_electives.*', 'stu.student_elective_approvals.*')));

DELETE FROM role_template_permissions
WHERE (template_code = 'teacher' AND permission_code IN ('stu.student_electives.create', 'stu.student_electives.edit', 'stu.student_elective_approvals.create'))
   OR (template_code = 'principal' AND permission_code IN ('stu.student_electives.*', 'stu.student_elective_approvals.*'));

DROP TABLE student_elective_choice_history;
DROP TABLE student_elective_changes;
DROP TABLE student_elective_choice_subjects;
DROP TABLE student_elective_choices;

DROP INDEX subject_class_mappings_group_idx;
ALTER TABLE subject_class_mappings DROP CONSTRAINT subject_class_mappings_group_needs_elective_ck;
ALTER TABLE subject_class_mappings DROP CONSTRAINT subject_class_mappings_elective_group_fk;
ALTER TABLE subject_class_mappings DROP COLUMN elective_group_id;
