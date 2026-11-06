CREATE INDEX auth_events_last_login ON auth_events(user_id, occurred_at DESC) WHERE action = 'login' AND outcome = 'success';
