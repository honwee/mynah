-- Channel branding for third-party customization (三方定制):
--   brand_logo — top-bar logo image URL shown in place of the built-in
--                soundwave mark, so a reseller can hand the channel link to
--                THEIR customers under their own brand;
--   bg_image   — stage background image URL. The visitor page chroma-keys the
--                green-screen avatar (WebGL) over this image; '' = keep the
--                default stage backdrop and skip keying.
-- Same family as brand_name/theme_color (display metadata, hot-editable, not
-- part of the frozen persona snapshot).
ALTER TABLE channels
    ADD COLUMN brand_logo TEXT NOT NULL DEFAULT '',
    ADD COLUMN bg_image   TEXT NOT NULL DEFAULT '';
