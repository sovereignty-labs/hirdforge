from fastapi import FastAPI
from sentence_transformers import SentenceTransformer
import torch
import uvicorn

app = FastAPI()
device = "cuda" if torch.cuda.is_available() else "cpu"
model = SentenceTransformer("all-MiniLM-L6-v2", device=device)

@app.post("/embed")
async def embed(request: dict):
    texts = request.get("texts", [])
    with torch.no_grad():
        embeddings = model.encode(texts, normalize_embeddings=True).tolist()
    return {"embeddings": embeddings}

@app.get("/health")
async def health():
    return {"status": "ok", "device": device, "model": "all-MiniLM-L6-v2"}

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=8003)
