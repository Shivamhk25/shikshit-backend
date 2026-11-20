-- 000006_reference_data: plans, core permission catalog, default role templates. Idempotent.
-- The complete permission catalog is generated from docs/plan/backlog.csv by FND-06 tooling; only the platform-level
-- modules (identity, approvals, onboarding) are seeded here. Templates use wildcard patterns, so they keep working
-- as the catalog grows. Role defaults follow docs/plan/01-access-model.md §5 and are editable per institute.

INSERT INTO plans (code, name, modules) VALUES
    ('standard',   'Standard',   ARRAY['iam','wfl','onb','aca','adm','stu','fee','att','exm','asg','crt','slf','stf','com','dsh','rpt']),
    ('premium',    'Premium',    ARRAY['iam','wfl','onb','aca','adm','stu','fee','att','exm','asg','crt','slf','stf','com','dsh','rpt','lrn','awd','cns','trn','lib','res','caf','ops','hrp','inv','sec','hlt','dev']),
    ('enterprise', 'Enterprise', ARRAY['iam','wfl','onb','aca','adm','stu','fee','att','exm','asg','crt','slf','stf','com','dsh','rpt','lrn','awd','cns','trn','lib','res','caf','ops','hrp','inv','sec','hlt','dev','aia'])
ON CONFLICT (code) DO NOTHING;

-- ---------------------------------------------------------------- core permission catalog
INSERT INTO permissions (code, module, resource, action, sensitive)
SELECT m || '.' || r || '.' || a, m, r, a, (m = 'onb' AND r = 'health')
FROM (VALUES
    ('iam', 'users',            ARRAY['view','create','edit','delete','export']),
    ('iam', 'roles',            ARRAY['view','create','edit','delete']),
    ('iam', 'scope_assignments',ARRAY['view','edit']),
    ('iam', 'sessions',         ARRAY['view','manage']),
    ('iam', 'access_review',    ARRAY['view','export']),
    ('iam', 'field_masks',      ARRAY['view','manage']),
    ('iam', 'delegations',      ARRAY['view','create','delete']),
    ('iam', 'tenant_access',    ARRAY['view','manage']),
    ('iam', 'parent_links',     ARRAY['view','create','edit','delete']),
    ('iam', 'sign_in_methods',  ARRAY['view','manage']),
    ('wfl', 'requests',         ARRAY['view','approve']),
    ('wfl', 'definitions',      ARRAY['view','manage']),
    ('onb', 'control_tower',    ARRAY['view']),
    ('onb', 'workspace',        ARRAY['view']),
    ('onb', 'tasks',            ARRAY['view','create','edit']),
    ('onb', 'templates',        ARRAY['view']),
    ('onb', 'imports',          ARRAY['view','import','commit','rollback','review']),
    ('onb', 'quality',          ARRAY['view','edit']),
    ('onb', 'media',            ARRAY['view','create']),
    ('onb', 'quick_add',        ARRAY['create']),
    ('onb', 'maker_checker',    ARRAY['view','manage']),
    ('onb', 'golive',           ARRAY['view','approve'])
) AS g(m, r, acts), LATERAL unnest(g.acts) AS a
ON CONFLICT (code) DO NOTHING;

-- Per-domain onboarding packs: onb.<domain>.{view,import,commit,rollback,review}
INSERT INTO permissions (code, module, resource, action, sensitive)
SELECT 'onb.' || d || '.' || a, 'onb', d, a, (d = 'health')
FROM unnest(ARRAY['academic','students','staff','fees','library','transport','residential','cafeteria','inventory','health','devices','communication','certificates']) AS d,
     unnest(ARRAY['view','import','commit','rollback','review']) AS a
ON CONFLICT (code) DO NOTHING;

-- ---------------------------------------------------------------- role templates
INSERT INTO role_templates (code, name, description, is_admin, sort_order) VALUES
    ('admin',             'Admin',              'Full visibility and management inside the institute. Read-only system role: clone it to customise.', true, 1),
    ('principal',         'Principal',          'Academic leadership: oversight, approvals, reports.', false, 10),
    ('registrar',         'Registrar',          'Admissions, student records, certificates and the students data-onboarding pack.', false, 20),
    ('accountant',        'Accountant',         'Fees, concessions, payments and the fees data-onboarding pack.', false, 30),
    ('teacher',           'Teacher',            'Own classes: attendance, marks, assignments, diary.', false, 40),
    ('hr_manager',        'HR Manager',         'Staff records, leave, payroll.', false, 50),
    ('librarian',         'Librarian',          'Library catalogue and circulation.', false, 60),
    ('transport_manager', 'Transport Manager',  'Routes, vehicles, trips, transport fees.', false, 70),
    ('driver',            'Driver',             'Assigned routes and trips.', false, 80),
    ('caretaker',         'Caretaker',          'Assigned routes: boarding and alighting.', false, 90),
    ('warden',            'Warden',             'Hostels, roll call, inspections, housekeeping.', false, 100),
    ('mess_manager',      'Mess Manager',       'Mess plans, menus, subscriptions.', false, 110),
    ('cashier',           'Cashier',            'Cafeteria point-of-sale.', false, 120),
    ('security_guard',    'Security Guard',     'Gate desk, passes, visitors.', false, 130),
    ('nurse',             'Nurse',              'Student health records and medical incidents.', false, 140),
    ('store_keeper',      'Store Keeper',       'Inventory, stores, assets, work orders.', false, 150),
    ('counselor',         'Counselor',          'Counseling and parent-teacher meetings.', false, 160),
    ('parent',            'Parent',             'Own children only.', false, 170),
    ('student',           'Student',            'Own records only.', false, 180)
ON CONFLICT (code) DO NOTHING;

-- Every data owner can use the shared onboarding tools; domain permissions (onb.<domain>.*) decide what they may load.
INSERT INTO role_template_permissions (template_code, permission_code, scope) VALUES
    -- admin: everything the institute has entitled
    ('admin', '*.*.*', 'tenant'),

    -- principal
    ('principal', 'aca.*.*', 'tenant'), ('principal', 'adm.*.view', 'tenant'), ('principal', 'stu.*.view', 'tenant'),
    ('principal', 'fee.*.view', 'tenant'), ('principal', 'att.*.*', 'tenant'), ('principal', 'exm.*.*', 'tenant'),
    ('principal', 'asg.*.view', 'tenant'), ('principal', 'crt.*.*', 'tenant'), ('principal', 'cns.*.view', 'tenant'),
    ('principal', 'awd.*.*', 'tenant'), ('principal', 'lib.*.view', 'tenant'), ('principal', 'sec.*.view', 'tenant'),
    ('principal', 'hlt.*.view', 'tenant'), ('principal', 'res.*.view', 'tenant'), ('principal', 'caf.*.view', 'tenant'),
    ('principal', 'ops.*.*', 'tenant'), ('principal', 'hrp.*.view', 'tenant'), ('principal', 'inv.*.view', 'tenant'),
    ('principal', 'trn.*.view', 'tenant'), ('principal', 'com.*.*', 'tenant'), ('principal', 'rpt.*.*', 'tenant'),
    ('principal', 'dsh.*.view', 'tenant'), ('principal', 'wfl.requests.*', 'tenant'), ('principal', 'onb.control_tower.view', 'tenant'),
    ('principal', 'onb.golive.view', 'tenant'), ('principal', 'stf.*.*', 'self'),

    -- registrar
    ('registrar', 'adm.*.*', 'tenant'), ('registrar', 'stu.*.*', 'tenant'), ('registrar', 'crt.*.*', 'tenant'),
    ('registrar', 'com.*.*', 'tenant'), ('registrar', 'aca.*.view', 'tenant'), ('registrar', 'exm.*.view', 'tenant'),
    ('registrar', 'att.*.view', 'tenant'), ('registrar', 'fee.*.view', 'tenant'), ('registrar', 'rpt.*.view', 'tenant'),
    ('registrar', 'onb.students.*', 'tenant'), ('registrar', 'onb.certificates.*', 'tenant'),
    ('registrar', 'onb.workspace.view', 'tenant'), ('registrar', 'onb.tasks.*', 'tenant'), ('registrar', 'onb.templates.view', 'tenant'),
    ('registrar', 'onb.imports.*', 'tenant'), ('registrar', 'onb.quality.*', 'tenant'), ('registrar', 'onb.media.*', 'tenant'),
    ('registrar', 'onb.quick_add.*', 'tenant'), ('registrar', 'stf.*.*', 'self'),

    -- accountant
    ('accountant', 'fee.*.*', 'tenant'), ('accountant', 'adm.*.view', 'tenant'), ('accountant', 'stu.*.view', 'tenant'),
    ('accountant', 'caf.*.view', 'tenant'), ('accountant', 'inv.*.view', 'tenant'), ('accountant', 'trn.*.view', 'tenant'),
    ('accountant', 'rpt.*.*', 'tenant'), ('accountant', 'onb.fees.*', 'tenant'),
    ('accountant', 'onb.workspace.view', 'tenant'), ('accountant', 'onb.tasks.*', 'tenant'), ('accountant', 'onb.templates.view', 'tenant'),
    ('accountant', 'onb.imports.*', 'tenant'), ('accountant', 'onb.quality.*', 'tenant'), ('accountant', 'onb.quick_add.*', 'tenant'),
    ('accountant', 'stf.*.*', 'self'),

    -- teacher: own classes and sections
    ('teacher', 'att.*.*', 'class_section'), ('teacher', 'exm.*.*', 'class_section'), ('teacher', 'asg.*.*', 'class_section'),
    ('teacher', 'aca.*.view', 'class_section'), ('teacher', 'stu.*.view', 'class_section'), ('teacher', 'crt.*.view', 'class_section'),
    ('teacher', 'cns.*.*', 'class_section'), ('teacher', 'lrn.*.*', 'class_section'), ('teacher', 'awd.*.*', 'class_section'),
    ('teacher', 'com.*.*', 'class_section'), ('teacher', 'lib.*.view', 'tenant'), ('teacher', 'ops.*.view', 'owned'),
    ('teacher', 'stf.*.*', 'self'),

    -- hr_manager
    ('hr_manager', 'hrp.*.*', 'tenant'), ('hr_manager', 'att.*.view', 'tenant'), ('hr_manager', 'aca.*.view', 'tenant'),
    ('hr_manager', 'rpt.*.view', 'tenant'), ('hr_manager', 'wfl.requests.*', 'tenant'), ('hr_manager', 'onb.staff.*', 'tenant'),
    ('hr_manager', 'onb.workspace.view', 'tenant'), ('hr_manager', 'onb.tasks.*', 'tenant'), ('hr_manager', 'onb.templates.view', 'tenant'),
    ('hr_manager', 'onb.imports.*', 'tenant'), ('hr_manager', 'onb.quality.*', 'tenant'), ('hr_manager', 'onb.quick_add.*', 'tenant'),
    ('hr_manager', 'stf.*.*', 'self'),

    -- librarian
    ('librarian', 'lib.*.*', 'tenant'), ('librarian', 'stu.*.view', 'tenant'), ('librarian', 'onb.library.*', 'tenant'),
    ('librarian', 'onb.workspace.view', 'tenant'), ('librarian', 'onb.tasks.*', 'tenant'), ('librarian', 'onb.templates.view', 'tenant'),
    ('librarian', 'onb.imports.*', 'tenant'), ('librarian', 'onb.quality.*', 'tenant'), ('librarian', 'onb.quick_add.*', 'tenant'),
    ('librarian', 'stf.*.*', 'self'),

    -- transport
    ('transport_manager', 'trn.*.*', 'tenant'), ('transport_manager', 'stu.*.view', 'tenant'), ('transport_manager', 'onb.transport.*', 'tenant'),
    ('transport_manager', 'onb.workspace.view', 'tenant'), ('transport_manager', 'onb.tasks.*', 'tenant'), ('transport_manager', 'onb.templates.view', 'tenant'),
    ('transport_manager', 'onb.imports.*', 'tenant'), ('transport_manager', 'onb.quality.*', 'tenant'), ('transport_manager', 'onb.quick_add.*', 'tenant'),
    ('transport_manager', 'stf.*.*', 'self'),
    ('driver', 'trn.*.view', 'assigned_route'), ('driver', 'trn.*.edit', 'assigned_route'), ('driver', 'stf.*.*', 'self'),
    ('caretaker', 'trn.*.view', 'assigned_route'), ('caretaker', 'trn.*.edit', 'assigned_route'), ('caretaker', 'stf.*.*', 'self'),

    -- residential, cafeteria
    ('warden', 'res.*.*', 'assigned_hostel'), ('warden', 'stu.*.view', 'assigned_hostel'), ('warden', 'hlt.*.view', 'assigned_hostel'),
    ('warden', 'ops.*.*', 'tenant'), ('warden', 'onb.residential.*', 'tenant'),
    ('warden', 'onb.workspace.view', 'tenant'), ('warden', 'onb.tasks.*', 'tenant'), ('warden', 'onb.templates.view', 'tenant'),
    ('warden', 'onb.imports.*', 'tenant'), ('warden', 'onb.quality.*', 'tenant'), ('warden', 'onb.quick_add.*', 'tenant'), ('warden', 'stf.*.*', 'self'),
    ('mess_manager', 'caf.*.*', 'tenant'), ('mess_manager', 'onb.cafeteria.*', 'tenant'),
    ('mess_manager', 'onb.workspace.view', 'tenant'), ('mess_manager', 'onb.tasks.*', 'tenant'), ('mess_manager', 'onb.templates.view', 'tenant'),
    ('mess_manager', 'onb.imports.*', 'tenant'), ('mess_manager', 'onb.quality.*', 'tenant'), ('mess_manager', 'onb.quick_add.*', 'tenant'), ('mess_manager', 'stf.*.*', 'self'),
    ('cashier', 'caf.*.*', 'tenant'), ('cashier', 'stf.*.*', 'self'),

    -- security, health, stores, counseling
    ('security_guard', 'sec.*.*', 'tenant'), ('security_guard', 'stu.*.view', 'tenant'), ('security_guard', 'stf.*.*', 'self'),
    ('nurse', 'hlt.*.*', 'tenant'), ('nurse', 'stu.*.view', 'tenant'), ('nurse', 'onb.health.*', 'tenant'),
    ('nurse', 'onb.workspace.view', 'tenant'), ('nurse', 'onb.tasks.*', 'tenant'), ('nurse', 'onb.templates.view', 'tenant'),
    ('nurse', 'onb.imports.*', 'tenant'), ('nurse', 'onb.quality.*', 'tenant'), ('nurse', 'onb.quick_add.*', 'tenant'), ('nurse', 'stf.*.*', 'self'),
    ('store_keeper', 'inv.*.*', 'tenant'), ('store_keeper', 'ops.*.*', 'tenant'), ('store_keeper', 'onb.inventory.*', 'tenant'),
    ('store_keeper', 'onb.workspace.view', 'tenant'), ('store_keeper', 'onb.tasks.*', 'tenant'), ('store_keeper', 'onb.templates.view', 'tenant'),
    ('store_keeper', 'onb.imports.*', 'tenant'), ('store_keeper', 'onb.quality.*', 'tenant'), ('store_keeper', 'onb.quick_add.*', 'tenant'), ('store_keeper', 'stf.*.*', 'self'),
    ('counselor', 'cns.*.*', 'tenant'), ('counselor', 'stu.*.view', 'tenant'), ('counselor', 'stf.*.*', 'self'),

    -- families
    ('parent',  'slf.*.*', 'own_children'), ('parent',  'com.*.view', 'own_children'),
    ('student', 'slf.*.*', 'self'), ('student', 'lrn.*.view', 'self'), ('student', 'lib.*.view', 'self'), ('student', 'com.*.view', 'self')
ON CONFLICT (template_code, permission_code) DO NOTHING;
