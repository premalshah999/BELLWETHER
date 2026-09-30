-- A registrant whose whole name is a market term -- Dow, Nasdaq, Crypto,
-- Bullish, Citizens -- was attached to every headline using the word, so each
-- market wrap was filed under Dow Inc. The resolver no longer matches these
-- words alone; this removes the tags already stored, keeping a headline that
-- gives the company's legal form. The events themselves stay.
DELETE FROM event_entities ee
USING events e
WHERE e.id = ee.event_id
  AND ee.match_method = 'legal_name'
  AND ee.match_confidence BETWEEN 0.915 AND 0.925
  AND ee.symbol IN ('DOW', 'NDAQ', 'CRCW', 'BLSH', 'SE', 'CIA', 'NOV', 'PAYD', 'ESGH', 'TBBK', 'WNRS', 'RS')
  AND e.headline !~* '\m(dow|nasdaq|sea|citizens|bullish|reliance)[,.]? (inc|corp|ltd|limited|steel)';
