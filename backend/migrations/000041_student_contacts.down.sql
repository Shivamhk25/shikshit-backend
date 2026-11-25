DELETE FROM role_template_permissions WHERE permission_code = 'stu.student_contacts.view';
DELETE FROM role_permissions WHERE permission_code = 'stu.student_contacts.view';
DELETE FROM user_permission_overrides WHERE permission_code = 'stu.student_contacts.view';
DELETE FROM permissions WHERE code = 'stu.student_contacts.view';
