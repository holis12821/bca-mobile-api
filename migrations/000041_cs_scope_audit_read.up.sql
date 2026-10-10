-- Dua cakupan baru, dan satu jenis peristiwa audit baru.
--
-- Keduanya menutup selisih yang tercatat di .claude/skills/cs-desktop-workflow-backend
-- §3.7, dan satu-satunya alasan keduanya menumpang satu migrasi adalah bahwa keduanya
-- melebarkan CHECK yang sama-sama dikunci di 000027 dan 000037: dua migrasi berurutan
-- yang masing-masing DROP lalu ADD constraint di tabel yang sama mengambil ACCESS
-- EXCLUSIVE dua kali untuk satu perubahan kewenangan.
--
-- AUDIT_READ — sebelum ini `GET /internal/v1/cs/audit-events` hanya menuntut identitas
-- petugas, jadi SETIAP petugas terautentikasi bisa membaca jejak rekannya: jam login,
-- loket, dan setiap otorisasi supervisor yang pernah gagal atas namanya. Jejak audit yang
-- bisa dibaca semua orang yang diawasinya bukan pembatas kewenangan, ia hanya terlihat
-- seperti pembatas.
--
-- ESCALATION_REVIEW — penyelesaian perkara NEED_REVIEW adalah pekerjaan Tier 2, dan ia
-- sengaja TIDAK memakai VIDEO_CALL: petugas yang mengaku tidak sanggup memutuskan sebuah
-- verifikasi tidak boleh jadi orang yang menutup perkaranya sendiri. Larangan
-- menutup-sendiri ditegakkan di service, tapi cakupan terpisah inilah yang membuat
-- sebagian besar petugas tidak bisa menyentuh jalurnya sama sekali.
ALTER TABLE cs_agents
    DROP CONSTRAINT cs_agents_scopes_valid;

ALTER TABLE cs_agents
    ADD CONSTRAINT cs_agents_scopes_valid CHECK (
        cardinality(scopes) > 0
        AND scopes <@ ARRAY[
            'VIDEO_CALL',
            'CARD_ADMIN',
            'CUSTOMER_PII',
            'TICKET',
            'AUDIT_READ',
            'ESCALATION_REVIEW'
        ]::TEXT[]
    );

-- AGENT_UPDATED: perubahan cakupan atau pencabutan petugas lewat
-- `PATCH /internal/v1/agents/{employee_id}`.
--
-- Masuk cs_audit_events, bukan onboarding_audit_logs: ia tentang kewenangan seorang
-- PETUGAS dan tidak punya session_id nasabah sama sekali. Penyelesaian eskalasi justru
-- sebaliknya — ia tentang sesi seorang nasabah, jadi ia ditulis ke onboarding_audit_logs
-- sebagai VIDEO_CALL_ESCALATION_RESOLVED, dan event_type di sana tidak ber-CHECK.
ALTER TABLE cs_audit_events
    DROP CONSTRAINT cs_audit_events_type_valid;

ALTER TABLE cs_audit_events
    ADD CONSTRAINT cs_audit_events_type_valid CHECK (
        event_type IN (
            'AGENT_REGISTERED',
            'AGENT_UPDATED',
            'AGENT_LOGIN',
            'AGENT_LOGIN_FAILED',
            'AGENT_LOGOUT',
            'AGENT_PASSWORD_SET',
            'SUPERVISOR_AUTHORIZED',
            'SUPERVISOR_AUTH_FAILED',
            'DEVICE_HEALTHCHECK',
            'PII_ACKNOWLEDGED',
            'TERMINAL_REGISTERED',
            'TERMINAL_ACTIVATED',
            'TERMINAL_DEACTIVATED'
        )
    );
