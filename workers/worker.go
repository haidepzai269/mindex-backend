package workers

import (
	"context"
	"encoding/json"
	"log"
	"mindex-backend/config"
	"mindex-backend/controllers"
	"time"
)

const NumWorkers = 2

func StartWorkerPool() {
	for i := 0; i < NumWorkers; i++ {
		go runWorker()
	}
	log.Printf("Bắt đầu khởi động %d Upload Queue Workers", NumWorkers)
}

func runWorker() {
	for {
		// Kiểm tra nếu RedisClient nil (chưa kết nối được)
		if config.RedisClient == nil {
			log.Println("⚠️ Redis chưa sẵn sàng, đang đợi kết nối...")
			time.Sleep(5 * time.Second)
			continue
		}

		// BLPOP block indefinitely until a new job is pushed to "upload_queue"
		result, err := config.RedisClient.BLPop(context.Background(), 0, "upload_queue").Result()
		if err != nil {
			log.Println(" Worker queue pop error:", err)
			time.Sleep(2 * time.Second) // Giảm tải nếu kết nối đứt
			continue
		}

		if len(result) < 2 {
			continue
		}

		jobData := result[1]
		var job controllers.UploadJob
		if err := json.Unmarshal([]byte(jobData), &job); err != nil {
			log.Println("Worker json parse error:", err)
			continue
		}

		log.Printf("🚀 Bắt đầu xử lý DocumentID: %s", job.DocID)

		// Cập nhật trạng thái sang processing
		config.DB.Exec(config.Ctx, `UPDATE documents SET status='processing' WHERE id=$1`, job.DocID)

		err = RunEmbeddingPipeline(job)
		if err != nil {
			log.Printf("❌ Xử lý thất bại Doc %s: %v", job.DocID, err)
			config.DB.Exec(config.Ctx, `UPDATE documents SET status='error' WHERE id=$1`, job.DocID)
		} else {
			log.Printf("✅ Hoàn tất xử lý Doc %s", job.DocID)
		}
	}
}
		
