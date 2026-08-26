-- A small document that describes its own presentation, used to exercise
-- _style, _nav, views, blobs, NULLs and non-ASCII text.
CREATE TABLE _style(key TEXT, value TEXT);
INSERT INTO _style VALUES('title','Q3 Field Report'),('accent','#0f9d58'),('theme','auto');

CREATE TABLE _nav(table_name TEXT, label TEXT, position INTEGER, hidden INTEGER);
INSERT INTO _nav VALUES('readings','Sensor Readings',1,0),('sites','Field Sites',2,0),('scratch','',9,1);

CREATE TABLE sites(id INTEGER PRIMARY KEY, name TEXT NOT NULL, lat REAL, lon REAL,
                   opened TEXT, photo BLOB, notes TEXT);
INSERT INTO sites(name,lat,lon,opened,photo,notes) VALUES
 ('Cerro Verde',-12.0464,-77.0428,'2024-03-01',randomblob(2048),'Ridge station. Solar array replaced in June; telemetry stable since.'),
 ('Playa Norte',10.4806,-66.9036,'2024-05-17',randomblob(15300),NULL),
 ('Alto MirADOR',4.7110,-74.0721,'2025-01-09',NULL,'Intermittent packet loss between 02:00 and 04:00 local.'),
 ('Bahía Sur',-34.6037,-58.3816,'2025-06-30',randomblob(880),'Awaiting permit renewal.');

CREATE TABLE readings(id INTEGER PRIMARY KEY, site_id INTEGER, taken_at TEXT,
                      temp_c REAL, humidity REAL, pressure_hpa REAL, flag TEXT);
INSERT INTO readings(site_id,taken_at,temp_c,humidity,pressure_hpa,flag)
SELECT (n%4)+1, datetime(1751328000+n*900,'unixepoch'),
       round(14+(n%230)/9.0,2), round(40+(n%55),1), round(990+(n%40)/1.7,2),
       CASE WHEN n%53=0 THEN 'suspect' WHEN n%211=0 THEN 'calibration' ELSE NULL END
FROM (WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n<40000) SELECT n FROM c);

CREATE VIEW site_summary AS
  SELECT s.name, count(r.id) AS readings, round(avg(r.temp_c),2) AS avg_temp
  FROM sites s LEFT JOIN readings r ON r.site_id=s.id GROUP BY s.id;

CREATE TABLE scratch(k TEXT, v TEXT);
