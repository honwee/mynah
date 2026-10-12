-- Action-clip bakes. The same LivePortrait pipeline that bakes an avatar's idle
-- loop can bake a short one-shot gesture (shake / laugh / shy / wink ...) from a
-- different driving template; kind='action' + action=<clip id> tells the apply
-- step to install the asset as that avatar's action clip instead of its idle.
ALTER TABLE avatar_bake_jobs ADD COLUMN IF NOT EXISTS kind   TEXT NOT NULL DEFAULT 'idle';
ALTER TABLE avatar_bake_jobs ADD COLUMN IF NOT EXISTS action TEXT NOT NULL DEFAULT '';
