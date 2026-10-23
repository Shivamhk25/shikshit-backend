-- 000007_communication.up.sql
-- Communication module: announcements, messaging, notifications, calendar

-- Announcements
CREATE TABLE announcements (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    
    title VARCHAR(300) NOT NULL,
    content TEXT NOT NULL,
    content_format VARCHAR(20) DEFAULT 'markdown', -- markdown, html, plain
    summary VARCHAR(500),
    
    type VARCHAR(30) DEFAULT 'general', -- general, academic, event, emergency, holiday, maintenance
    priority VARCHAR(20) DEFAULT 'normal', -- low, normal, high, urgent
    
    -- Audience
    target_roles user_role[],              -- NULL = all roles
    target_grades INTEGER[],               -- NULL = all grades
    target_departments UUID[],             -- NULL = all departments
    target_users UUID[],                   -- Specific users (overrides roles)
    exclude_users UUID[],                  -- Users to exclude
    
    -- Channels
    send_email BOOLEAN DEFAULT false,
    send_push BOOLEAN DEFAULT true,
    send_sms BOOLEAN DEFAULT false,
    
    -- Scheduling
    publish_at TIMESTAMPTZ NOT NULL,
    expire_at TIMESTAMPTZ,
    is_pinned BOOLEAN DEFAULT false,
    pin_order INTEGER DEFAULT 0,
    
    -- Status
    status VARCHAR(20) DEFAULT 'draft', -- draft, scheduled, published, archived
    published_at TIMESTAMPTZ,
    archived_at TIMESTAMPTZ,
    
    -- Attachments
    attachments JSONB DEFAULT '[]'::jsonb, -- [{"name": "", "url": "", "size": 0, "type": ""}]
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE announcements IS 'Institute-wide announcements and notifications';
CREATE INDEX idx_announcements_institute ON announcements(institute_id, status, publish_at);
CREATE INDEX idx_announcements_audience ON announcements USING GIN (target_roles, target_grades, target_departments);
CREATE INDEX idx_announcements_published ON announcements(publish_at) WHERE status = 'published';

-- Announcement reads
CREATE TABLE announcement_reads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    announcement_id UUID NOT NULL REFERENCES announcements(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    read_at TIMESTAMPTZ DEFAULT now(),
    read_via VARCHAR(20) DEFAULT 'web', -- web, mobile, email, push
    UNIQUE (announcement_id, user_id)
);

COMMENT ON TABLE announcement_reads IS 'Track announcement read receipts';
CREATE INDEX idx_announcement_reads_user ON announcement_reads(user_id, read_at DESC);

-- Conversations (1-on-1 and group messaging)
CREATE TABLE conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    
    type VARCHAR(20) DEFAULT 'direct', -- direct, group, class, announcement
    name VARCHAR(200),                 -- Group name (null for direct)
    description TEXT,
    avatar_url VARCHAR(500),
    
    -- For class conversations
    class_id UUID REFERENCES classes(id) ON DELETE SET NULL,
    
    -- Settings
    is_archived BOOLEAN DEFAULT false,
    is_muted BOOLEAN DEFAULT false,
    allow_member_invite BOOLEAN DEFAULT true,
    allow_file_share BOOLEAN DEFAULT true,
    max_members INTEGER DEFAULT 100,
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE conversations IS 'Messaging conversations (direct and group)';
CREATE INDEX idx_conversations_institute ON conversations(institute_id, is_archived);

-- Conversation participants
CREATE TABLE conversation_participants (
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role VARCHAR(20) DEFAULT 'member', -- admin, moderator, member
    joined_at TIMESTAMPTZ DEFAULT now(),
    left_at TIMESTAMPTZ,
    last_read_at TIMESTAMPTZ,
    is_muted BOOLEAN DEFAULT false,
    notifications_enabled BOOLEAN DEFAULT true,
    PRIMARY KEY (conversation_id, user_id)
);

COMMENT ON TABLE conversation_participants IS 'Conversation membership';
CREATE INDEX idx_conv_participants_user ON conversation_participants(user_id, left_at) WHERE left_at IS NULL;

-- Messages
CREATE TABLE messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    sender_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reply_to_id UUID REFERENCES messages(id) ON DELETE SET NULL,
    
    content TEXT NOT NULL,
    content_format VARCHAR(20) DEFAULT 'plain', -- plain, markdown, html
    
    type VARCHAR(20) DEFAULT 'text', -- text, image, file, audio, video, location, contact, system
    file_urls TEXT[],
    file_metadata JSONB DEFAULT '{}'::jsonb,
    
    -- Status
    is_deleted BOOLEAN DEFAULT false,
    deleted_at TIMESTAMPTZ,
    deleted_by UUID REFERENCES users(id) ON DELETE SET NULL,
    is_edited BOOLEAN DEFAULT false,
    edited_at TIMESTAMPTZ,
    
    -- Delivery
    sent_at TIMESTAMPTZ DEFAULT now(),
    delivered_at TIMESTAMPTZ,
    read_at TIMESTAMPTZ,
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE messages IS 'Messages within conversations';
CREATE INDEX idx_messages_conversation ON messages(conversation_id, sent_at DESC);
CREATE INDEX idx_messages_sender ON messages(sender_id, sent_at DESC);
CREATE INDEX idx_messages_reply ON messages(reply_to_id);

-- Message reads (for group chats)
CREATE TABLE message_reads (
    message_id UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    read_at TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (message_id, user_id)
);

COMMENT ON TABLE message_reads IS 'Read receipts for group messages';

-- Notifications (in-app notification center)
CREATE TABLE notifications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    type VARCHAR(50) NOT NULL,          -- assignment_due, grade_posted, announcement, message, leave_approved, fee_due, etc.
    category VARCHAR(30) DEFAULT 'info', -- info, success, warning, error, academic, finance, hr
    
    title VARCHAR(300) NOT NULL,
    body TEXT,
    action_url VARCHAR(500),
    action_label VARCHAR(100),
    icon VARCHAR(100),
    
    -- Related entity
    related_type VARCHAR(50),           -- assignment, grade, announcement, conversation, invoice, etc.
    related_id UUID,
    
    -- Priority
    priority VARCHAR(20) DEFAULT 'normal', -- low, normal, high, urgent
    
    -- Status
    is_read BOOLEAN DEFAULT false,
    read_at TIMESTAMPTZ,
    is_archived BOOLEAN DEFAULT false,
    archived_at TIMESTAMPTZ,
    
    -- Delivery channels
    push_sent BOOLEAN DEFAULT false,
    push_sent_at TIMESTAMPTZ,
    email_sent BOOLEAN DEFAULT false,
    email_sent_at TIMESTAMPTZ,
    sms_sent BOOLEAN DEFAULT false,
    sms_sent_at TIMESTAMPTZ,
    
    expires_at TIMESTAMPTZ,
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE notifications IS 'In-app notification center';
CREATE INDEX idx_notifications_user ON notifications(user_id, is_read, created_at DESC);
CREATE INDEX idx_notifications_type ON notifications(type, created_at DESC);
CREATE INDEX idx_notifications_unread ON notifications(user_id, is_read) WHERE is_read = false;

-- Notification preferences
CREATE TABLE notification_preferences (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    
    -- Channel preferences
    email_enabled BOOLEAN DEFAULT true,
    push_enabled BOOLEAN DEFAULT true,
    sms_enabled BOOLEAN DEFAULT false,
    in_app_enabled BOOLEAN DEFAULT true,
    
    -- Category preferences (JSON for flexibility)
    email_categories JSONB DEFAULT '{"all": true}'::jsonb,
    push_categories JSONB DEFAULT '{"all": true}'::jsonb,
    sms_categories JSONB DEFAULT '{"urgent": true}'::jsonb,
    in_app_categories JSONB DEFAULT '{"all": true}'::jsonb,
    
    -- Quiet hours
    quiet_hours_enabled BOOLEAN DEFAULT false,
    quiet_hours_start TIME DEFAULT '22:00',
    quiet_hours_end TIME DEFAULT '07:00',
    timezone VARCHAR(50) DEFAULT 'UTC',
    
    -- Frequency
    digest_frequency VARCHAR(20) DEFAULT 'none', -- none, daily, weekly
    digest_time TIME DEFAULT '08:00',
    
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE notification_preferences IS 'User notification channel preferences';

-- Calendar events
CREATE TABLE calendar_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    institute_id UUID NOT NULL REFERENCES institutes(id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    
    title VARCHAR(300) NOT NULL,
    description TEXT,
    location VARCHAR(500),
    is_virtual BOOLEAN DEFAULT false,
    meeting_url VARCHAR(500),
    
    start_at TIMESTAMPTZ NOT NULL,
    end_at TIMESTAMPTZ NOT NULL,
    all_day BOOLEAN DEFAULT false,
    timezone VARCHAR(50) DEFAULT 'UTC',
    
    -- Recurrence
    recurrence_rule TEXT,               -- RRULE format (RFC 5545)
    recurrence_end DATE,
    recurrence_exceptions DATE[],
    parent_event_id UUID REFERENCES calendar_events(id) ON DELETE SET NULL,
    
    -- Audience (similar to announcements)
    target_roles user_role[],
    target_grades INTEGER[],
    target_departments UUID[],
    target_users UUID[],
    
    -- For class events
    class_id UUID REFERENCES classes(id) ON DELETE SET NULL,
    
    -- Type
    event_type VARCHAR(30) DEFAULT 'event', -- event, holiday, exam, deadline, meeting, ptm
    
    -- Status
    status VARCHAR(20) DEFAULT 'confirmed', -- tentative, confirmed, cancelled
    cancelled_at TIMESTAMPTZ,
    cancellation_reason TEXT,
    
    -- Reminders
    reminders JSONB DEFAULT '[{"minutes": 15, "channel": "push"}, {"minutes": 60, "channel": "email"}]'::jsonb,
    
    metadata JSONB DEFAULT '{}'::jsonb,
    
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

COMMENT ON TABLE calendar_events IS 'Institute calendar events';
COMMENT ON COLUMN calendar_events.recurrence_rule IS 'RFC 5545 RRULE for recurring events';
CREATE INDEX idx_calendar_institute ON calendar_events(institute_id, start_at, end_at);
CREATE INDEX idx_calendar_creator ON calendar_events(created_by);
CREATE INDEX idx_calendar_class ON calendar_events(class_id, start_at);

-- Event attendees
CREATE TABLE event_attendees (
    event_id UUID NOT NULL REFERENCES calendar_events(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(20) DEFAULT 'invited', -- invited, accepted, declined, tentative
    responded_at TIMESTAMPTZ,
    PRIMARY KEY (event_id, user_id)
);

COMMENT ON TABLE event_attendees IS 'Event RSVPs';
CREATE INDEX idx_event_attendees_user ON event_attendees(user_id, status);

-- Enable RLS
ALTER TABLE announcements ENABLE ROW LEVEL SECURITY;
ALTER TABLE announcement_reads ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversations ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversation_participants ENABLE ROW LEVEL SECURITY;
ALTER TABLE messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE message_reads ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE calendar_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE event_attendees ENABLE ROW LEVEL SECURITY;

-- RLS Policies
CREATE POLICY comm_tenant ON announcements
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY announcement_reads_tenant ON announcement_reads
    USING (announcement_id IN (SELECT id FROM announcements WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY conversations_tenant ON conversations
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY conv_participants_tenant ON conversation_participants
    USING (conversation_id IN (SELECT id FROM conversations WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY messages_tenant ON messages
    USING (conversation_id IN (SELECT id FROM conversations WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

CREATE POLICY message_reads_tenant ON message_reads
    USING (message_id IN (SELECT id FROM messages WHERE conversation_id IN (SELECT id FROM conversations WHERE institute_id = current_setting('app.current_institute_id', true)::UUID)));

CREATE POLICY notifications_tenant ON notifications
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY notif_prefs_tenant ON notification_preferences
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY calendar_tenant ON calendar_events
    USING (institute_id = current_setting('app.current_institute_id', true)::UUID);

CREATE POLICY event_attendees_tenant ON event_attendees
    USING (event_id IN (SELECT id FROM calendar_events WHERE institute_id = current_setting('app.current_institute_id', true)::UUID));

-- Self-access
CREATE POLICY announcement_reads_self ON announcement_reads
    USING (user_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY conv_participants_self ON conversation_participants
    USING (user_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY messages_self ON messages
    USING (sender_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY message_reads_self ON message_reads
    USING (user_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY notifications_self ON notifications
    USING (user_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY notif_prefs_self ON notification_preferences
    USING (user_id = current_setting('app.current_user_id', true)::UUID);

CREATE POLICY event_attendees_self ON event_attendees
    USING (user_id = current_setting('app.current_user_id', true)::UUID);