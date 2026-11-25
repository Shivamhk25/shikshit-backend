-- 000041_student_contacts: the permission that unmasks contact details on the student identity card (STU-01).
-- Without it a user with stu.student_identity.view still finds a student and sees the guardians' names and relations,
-- but guardian phone numbers and emails (and the emergency number on the ID card) come back empty.
--
-- Who holds it by default, with no new grants written:
--   * the Admin and the Registrar (their wildcards), the Principal, the Teacher (class sections only) and the Warden
--     (their stu.*.view wildcards cover it);
--   * not the accountant, librarian, transport manager, security guard, nurse and counselor: 000035 gave them explicit
--     stu view grants, which do not include a permission that did not exist then. The school can grant it to a role.
-- Whether those roles should see parents' numbers is the founder's call (APPROVALS A-27).

INSERT INTO permissions (code, module, resource, action, sensitive)
VALUES ('stu.student_contacts.view', 'stu', 'student_contacts', 'view', true)
ON CONFLICT (code) DO NOTHING;
