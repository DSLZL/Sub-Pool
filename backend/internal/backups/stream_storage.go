package backups

import (
	"context"
	"crypto/sha256"
	"fmt"
	stdhash "hash"
	"io"

	"github.com/jackc/pgx/v5"
)

type blobWriter struct {
	ctx   context.Context
	tx    pgx.Tx
	id    string
	max   int64
	size  int64
	part  int
	sum   stdhash.Hash
	chunk []byte
	err   error
}

func newBlobWriter(ctx context.Context, tx pgx.Tx, maxBytes int64) (*blobWriter, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO backup_blobs(id) VALUES($1)`, id); err != nil {
		return nil, err
	}
	return &blobWriter{ctx: ctx, tx: tx, id: id, max: maxBytes, sum: sha256.New(), chunk: make([]byte, 0, frameSize)}, nil
}

func (b *blobWriter) flush() error {
	if b.err != nil {
		return b.err
	}
	if len(b.chunk) != 0 {
		_, b.err = b.tx.Exec(b.ctx, `INSERT INTO backup_blob_chunks(blob_id,part,body) VALUES($1,$2,$3)`, b.id, b.part, b.chunk)
		b.part++
		b.chunk = b.chunk[:0]
	}
	return b.err
}

func (b *blobWriter) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if int64(len(p)) > b.max-b.size {
		b.err = ErrLimit
		return 0, b.err
	}
	for n := 0; n < len(p); {
		size := min(frameSize-len(b.chunk), len(p)-n)
		part := p[n : n+size]
		b.chunk = append(b.chunk, part...)
		b.size += int64(size)
		_, _ = b.sum.Write(part)
		n += size
		if len(b.chunk) == frameSize {
			if err := b.flush(); err != nil {
				return n, err
			}
		}
	}
	return len(p), nil
}

type blobReader struct {
	ctx    context.Context
	tx     pgx.Tx
	id     string
	size   int64
	sha256 string
	read   int64
	next   int
	sum    stdhash.Hash
	batch  [][]byte
}

func newBlobReader(ctx context.Context, tx pgx.Tx, id string, size int64, digest string) *blobReader {
	return &blobReader{ctx: ctx, tx: tx, id: id, size: size, sha256: digest, sum: sha256.New()}
}

func (b *blobReader) load() error {
	rows, err := b.tx.Query(b.ctx, `SELECT part,body FROM backup_blob_chunks WHERE blob_id=$1 AND part>=$2 ORDER BY part LIMIT 4`, b.id, b.next)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var part int
		var body []byte
		if err = rows.Scan(&part, &body); err != nil {
			return err
		}
		if part != b.next || len(body) == 0 || len(body) > frameSize {
			return ErrInvalid
		}
		b.batch = append(b.batch, body)
		b.next++
	}
	return rows.Err()
}

func (b *blobReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(b.batch) == 0 {
		if err := b.load(); err != nil {
			return 0, err
		}
		if len(b.batch) == 0 {
			if b.read != b.size || fmt.Sprintf("%x", b.sum.Sum(nil)) != b.sha256 {
				return 0, ErrInvalid
			}
			return 0, io.EOF
		}
	}
	chunk := b.batch[0]
	if int64(len(chunk)) > b.size-b.read {
		return 0, ErrInvalid
	}
	n := copy(p, chunk)
	_, _ = b.sum.Write(p[:n])
	b.read += int64(n)
	b.batch[0] = chunk[n:]
	if len(b.batch[0]) == 0 {
		b.batch[0] = nil
		b.batch = b.batch[1:]
	}
	return n, nil
}

func cleanExpiredUploads(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM backup_blob_chunks WHERE blob_id IN (
		SELECT p.blob_id FROM restore_plans p WHERE p.blob_id IS NOT NULL AND p.state<>'applying'
		  AND p.expires_at<=clock_timestamp()
		  AND EXISTS(SELECT 1 FROM backup_blob_chunks c WHERE c.blob_id=p.blob_id)
		  AND NOT EXISTS(SELECT 1 FROM backup_archives a WHERE a.blob_id=p.blob_id)
		FOR UPDATE SKIP LOCKED
	)`)
	return err
}
