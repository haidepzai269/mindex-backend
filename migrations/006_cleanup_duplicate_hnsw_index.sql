-- Remove the duplicate HNSW index on document_chunks.embedding.
-- Keep idx_document_chunks_embedding_hnsw because startup migration creates that name.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_chunks_embedding_hnsw;
