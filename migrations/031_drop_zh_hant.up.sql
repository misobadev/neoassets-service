-- m2m100 (Workers AI) only supports "zh" (simplified Chinese); traditional
-- Chinese (zh_hant) is dropped from the supported language set. Translations
-- already generated for zh_hant are removed and the lang row is disabled so it
-- stops appearing in the language selector.
DELETE FROM game_translations WHERE lang_code = 'zh_hant';
DELETE FROM system_translations WHERE lang_code = 'zh_hant';
UPDATE lang SET enabled = false WHERE code = 'zh_hant';