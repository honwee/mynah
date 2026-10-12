-- Half-body presenter support for the idle bake (应用型半身数字人). When an avatar
-- has a <id>.halfbody.png canvas + <id>.blend.npz (face-parsing paste-back mask)
-- alongside its source, the bake composites every generated 512 face back onto
-- that head-to-waist canvas (bake Stage C-prime) and the live worker does the
-- same at speak time — so idle and speaking frames share one 720x1280 canvas and
-- the switch is pop-free. These two paths are resolved server-side at create time
-- (like source_image/motion_pkl) and submitted to the worker /bake endpoint.
-- Empty = legacy 512 face avatar (full backward compatibility). Additive.

ALTER TABLE avatar_bake_jobs ADD COLUMN half_body TEXT NOT NULL DEFAULT '';  -- <id>.halfbody.png (== engine cond + paste-back body)
ALTER TABLE avatar_bake_jobs ADD COLUMN blend     TEXT NOT NULL DEFAULT '';  -- <id>.blend.npz (precomputed mask+crop_box+face_box)
