package workers

import (
	"errors"
	"fmt"
	"log"
	"mindex-backend/config"
	"mindex-backend/models"
	"mindex-backend/utils"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

const NumWorkers = 2

func StartWorkerPool() {
	for i := 0; i < NumWorkers; i++ {
		go runWorker(fmt.Sprintf("upload-worker-%s", uuid.NewString()))
	}
	log.Printf("Bat dau khoi dong %d Upload Queue Workers", NumWorkers)
}

func runWorker(workerID string) {
	for {
		job, found, err := claimNextUploadJob(workerID)
		if err != nil {
			log.Printf("[Upload Worker] claim error: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if !found {
			waitForUploadSignal()
			continue
		}

		processUploadJob(workerID, job)
	}
}

func processUploadJob(workerID string, job models.UploadJob) {
	log.Printf("[Upload Worker] start doc=%s job=%s attempt=%d/%d", job.DocID, job.ID, job.Attempts, job.MaxAttempts)
	_, _ = config.DB.Exec(config.Ctx, `
		UPDATE documents
		SET status='processing',
			processing_error_code=NULL,
			processing_error_message=NULL
		WHERE id=$1`, job.DocID)

	err := RunEmbeddingPipeline(job)
	if err != nil {
		handleUploadJobFailure(workerID, job, err)
	} else {
		markUploadJobSucceeded(job)
		log.Printf("[Upload Worker] completed doc=%s job=%s", job.DocID, job.ID)
	}

	utils.ClearUserCache("docs", job.UserID)
	utils.ClearUserCache("collections", job.UserID)
}

func claimNextUploadJob(workerID string) (models.UploadJob, bool, error) {
	var job models.UploadJob
	err := config.DB.QueryRow(config.Ctx, `
		WITH next_job AS (
			SELECT id
			FROM upload_jobs
			WHERE (status IN ('queued', 'retrying') AND next_run_at <= NOW())
			   OR (status = 'running' AND locked_at < NOW() - INTERVAL '15 minutes')
			ORDER BY
				CASE WHEN status = 'running' THEN 0 ELSE 1 END,
				next_run_at ASC,
				created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE upload_jobs j
		SET status='running',
			attempts=CASE WHEN j.status = 'running' THEN j.attempts ELSE j.attempts + 1 END,
			locked_at=NOW(),
			locked_by=$1,
			updated_at=NOW()
		FROM next_job
		WHERE j.id = next_job.id
		RETURNING j.id::text, j.document_id::text, j.user_id::text,
			COALESCE(j.local_path, ''),
			COALESCE(j.cloudinary_url, ''),
			COALESCE(j.cloudinary_public_id, ''),
			j.attempts,
			j.max_attempts,
			COALESCE(j.image_paths, '{}')`,
		workerID,
	).Scan(
		&job.ID,
		&job.DocID,
		&job.UserID,
		&job.LocalPath,
		&job.CloudinaryURL,
		&job.CloudinaryPublicID,
		&job.Attempts,
		&job.MaxAttempts,
		&job.ImagePaths,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.UploadJob{}, false, nil
	}
	if err != nil {
		return models.UploadJob{}, false, err
	}
	return job, true, nil
}

func waitForUploadSignal() {
	if config.RedisClient == nil {
		time.Sleep(3 * time.Second)
		return
	}
	_, err := config.RedisClient.BLPop(config.Ctx, 5*time.Second, config.Env.RedisQueueName).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		log.Printf("[Upload Worker] Redis wait error: %v", err)
		time.Sleep(1 * time.Second)
	}
}

func handleUploadJobFailure(workerID string, job models.UploadJob, err error) {
	pipelineErr := normalizePipelineError(err)
	log.Printf("[Upload Worker] failed doc=%s job=%s code=%s retryable=%v: %v", job.DocID, job.ID, pipelineErr.Code, pipelineErr.Retryable, err)

	if pipelineErr.Retryable && job.Attempts < job.MaxAttempts {
		delay := retryDelayForAttempt(job.Attempts)
		_, _ = config.DB.Exec(config.Ctx, `
			UPDATE upload_jobs
			SET status='retrying',
				next_run_at=NOW() + $1::interval,
				locked_at=NULL,
				locked_by=NULL,
				error_code=$2,
				error_message=$3,
				updated_at=NOW()
			WHERE id=$4`,
			postgresInterval(delay), pipelineErr.Code, pipelineErr.Message, job.ID)
		_, _ = config.DB.Exec(config.Ctx, `
			UPDATE documents
			SET status='queued',
				processing_error_code=$1,
				processing_error_message=$2
			WHERE id=$3`, pipelineErr.Code, fmt.Sprintf("Đang thử lại sau %s", delay), job.DocID)
		utils.UpdateDocProgressDetail(job.DocID, "queued", 0, fmt.Sprintf("Đang thử lại sau %s", delay), pipelineErr.Code)
		signalRetry(job.DocID)
		return
	}

	_, _ = config.DB.Exec(config.Ctx, `
		UPDATE upload_jobs
		SET status='failed',
			locked_at=NULL,
			locked_by=NULL,
			error_code=$1,
			error_message=$2,
			updated_at=NOW()
		WHERE id=$3`,
		pipelineErr.Code, pipelineErr.Message, job.ID)
	_, _ = config.DB.Exec(config.Ctx, `
		UPDATE documents
		SET status='error',
			processing_error_code=$1,
			processing_error_message=$2
		WHERE id=$3`,
		pipelineErr.Code, pipelineErr.Message, job.DocID)
	utils.UpdateDocProgressDetail(job.DocID, "error", 0, pipelineErr.Message, pipelineErr.Code)

	if job.CloudinaryPublicID != "" {
		if cleanupErr := utils.DestroyRawFromCloudinary(job.CloudinaryPublicID); cleanupErr != nil {
			log.Printf("[Upload Worker] Cloudinary cleanup failed for doc %s: %v", job.DocID, cleanupErr)
		}
	}

	_ = workerID
}

func markUploadJobSucceeded(job models.UploadJob) {
	_, _ = config.DB.Exec(config.Ctx, `
		UPDATE upload_jobs
		SET status='succeeded',
			locked_at=NULL,
			locked_by=NULL,
			error_code=NULL,
			error_message=NULL,
			updated_at=NOW()
		WHERE id=$1`, job.ID)
}

func normalizePipelineError(err error) *PipelineError {
	var pipelineErr *PipelineError
	if errors.As(err, &pipelineErr) {
		if pipelineErr.Code == "" {
			pipelineErr.Code = "PIPELINE_FAILED"
		}
		if pipelineErr.Message == "" {
			pipelineErr.Message = err.Error()
		}
		return pipelineErr
	}
	return &PipelineError{
		Code:      "PIPELINE_FAILED",
		Message:   err.Error(),
		Retryable: true,
		Err:       err,
	}
}

func retryDelayForAttempt(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 30 * time.Second
	case attempt == 2:
		return 2 * time.Minute
	default:
		return 10 * time.Minute
	}
}

func postgresInterval(delay time.Duration) string {
	return fmt.Sprintf("%d seconds", int(delay.Seconds()))
}

func signalRetry(docID string) {
	if config.RedisClient == nil {
		return
	}
	if err := config.RedisClient.RPush(config.Ctx, config.Env.RedisQueueName, docID).Err(); err != nil {
		log.Printf("[Upload Worker] Redis retry signal failed for doc %s: %v", docID, err)
	}
}
