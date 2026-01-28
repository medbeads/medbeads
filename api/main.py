from fastapi import FastAPI, HTTPException, Query
from pydantic import BaseModel
from typing import List, Optional, Dict, Any
import requests
from fastapi.middleware.cors import CORSMiddleware
from dotenv import load_dotenv
import os

# Load environment variables from .env file
load_dotenv()

app = FastAPI(title="MedBeads AI Server")

app.add_middleware(
    CORSMiddleware,
    allow_origins=["http://localhost:5173", "http://localhost:3000", "http://localhost:5174"], # Vite default
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

CORE_URL = "http://localhost:8080"

class BeadContent(BaseModel):
    text: Optional[str] = None
    message: Optional[str] = None
    # Add other fields as needed

class BeadCreate(BaseModel):
    type: str
    content: Dict[str, Any]
    parents: Optional[List[str]] = []

@app.get("/")
def read_root():
    return {"message": "MedBeads AI Server is running"}

@app.post("/beads")
def create_bead(bead: BeadCreate):
    try:
        response = requests.post(f"{CORE_URL}/beads", json=bead.model_dump())
        response.raise_for_status()
        return response.json()
    except requests.exceptions.RequestException as e:
        raise HTTPException(status_code=502, detail=f"Core Server Error: {str(e)}")

@app.get("/beads")
def get_bead(id: str = Query(..., description="Bead ID")):
    try:
        response = requests.get(f"{CORE_URL}/beads", params={"id": id})
        if response.status_code == 404:
            raise HTTPException(status_code=404, detail="Bead not found")
        response.raise_for_status()
        return response.json()
    except requests.exceptions.RequestException as e:
        raise HTTPException(status_code=502, detail=f"Core Server Error: {str(e)}")

@app.get("/beads/context")
@app.get("/beads/context")
def get_context(id: str = Query(..., description="Bead ID"), depth: int = 5, lookup: Optional[str] = None):
    try:
        params = {"id": id, "depth": depth}
        if lookup:
            params["lookup"] = lookup
        response = requests.get(f"{CORE_URL}/beads/context", params=params)
        response.raise_for_status()
        return response.json()
    except requests.exceptions.RequestException as e:
        raise HTTPException(status_code=502, detail=f"Core Server Error: {str(e)}")

@app.get("/patients")
def get_patients():
    try:
        response = requests.get(f"{CORE_URL}/patients")
        response.raise_for_status()
        return response.json()
    except requests.exceptions.RequestException as e:
        raise HTTPException(status_code=502, detail=f"Core Server Error: {str(e)}")

# --- AI Integration ---
import ai

class InsightRequest(BaseModel):
    target_bead_id: str

@app.post("/ai/insight")
def get_insight(request: InsightRequest):
    try:
        # 1. Fetch Target Bead
        target_res = requests.get(f"{CORE_URL}/beads", params={"id": request.target_bead_id})
        if target_res.status_code != 200:
            raise HTTPException(status_code=404, detail="Target Bead not found")
        target_bead = target_res.json()

        # 2. Fetch Context (Ancestors)
        # Using depth=10 to get a good history for AI
        context_res = requests.get(f"{CORE_URL}/beads/context", params={"id": request.target_bead_id, "depth": 10})
        if context_res.status_code != 200:
            # If context fetch fails, we might still proceed with just target, but context is key here.
            # Let's assume empty context if fail, or raise error.
            context_beads = []
        else:
            context_beads = context_res.json()

        # 3. Generate Insight
        result = ai.generate_insight(target_bead, context_beads)

        return {
            "insight": result["insight"],
            "beads_used": result["beads_used"]
        }

    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8000)
