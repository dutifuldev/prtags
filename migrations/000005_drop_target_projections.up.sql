DO $$
BEGIN
    IF to_regclass('river_job') IS NOT NULL THEN
        DELETE FROM river_job
        WHERE kind = 'target_projection_refresh'
           OR queue = 'target_projection_refresh';
    END IF;

    IF to_regclass('river_queue') IS NOT NULL THEN
        DELETE FROM river_queue
        WHERE name = 'target_projection_refresh';
    END IF;
END
$$;

DELETE FROM index_jobs
WHERE kind = 'target_projection_refresh';

DROP TABLE IF EXISTS target_projections;
