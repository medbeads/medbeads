from fastapi import FastAPI, HTTPException, Query, Request
from pydantic import BaseModel
from typing import List, Optional, Dict, Any
import requests
from fastapi.middleware.cors import CORSMiddleware
from dotenv import load_dotenv
import os

# Load environment variables from .env file
load_dotenv()

app = FastAPI(title="MedBeads AI Server")

# CORS origins are read from MEDBEADS_CORS_ORIGINS (comma-separated), with a
# localhost dev default. A single "*" entry restores allow-all behavior.
_cors_env = os.environ.get(
    "MEDBEADS_CORS_ORIGINS",
    "http://localhost:5173,http://localhost:5174,http://localhost:3000",
)
_cors_origins = [o.strip() for o in _cors_env.split(",") if o.strip()]

app.add_middleware(
    CORSMiddleware,
    allow_origins=_cors_origins,
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

CORE_URL = os.environ.get("CORE_URL", "http://localhost:8080")


def _forward_headers(request: Request) -> Dict[str, str]:
    """Forward the caller's access-control headers to the Core server so that
    clearance is enforced against the end user's roles, not the AI service."""
    headers: Dict[str, str] = {}
    for key in ("X-Viewer-Roles", "X-User-ID", "X-Service-Token", "X-Access-Reason"):
        value = request.headers.get(key)
        if value is not None:
            headers[key] = value
    return headers

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
def create_bead(bead: BeadCreate, request: Request):
    try:
        response = requests.post(f"{CORE_URL}/beads", json=bead.model_dump(), headers=_forward_headers(request))
        response.raise_for_status()
        return response.json()
    except requests.exceptions.RequestException as e:
        raise HTTPException(status_code=502, detail=f"Core Server Error: {str(e)}")

@app.get("/beads")
def get_bead(request: Request, id: str = Query(..., description="Bead ID")):
    try:
        response = requests.get(f"{CORE_URL}/beads", params={"id": id}, headers=_forward_headers(request))
        if response.status_code == 404:
            raise HTTPException(status_code=404, detail="Bead not found")
        response.raise_for_status()
        return response.json()
    except requests.exceptions.RequestException as e:
        raise HTTPException(status_code=502, detail=f"Core Server Error: {str(e)}")

@app.get("/beads/context")
def get_context(request: Request, id: str = Query(..., description="Bead ID"), depth: int = 5, lookup: Optional[str] = None):
    try:
        params = {"id": id, "depth": depth}
        if lookup:
            params["lookup"] = lookup
        response = requests.get(f"{CORE_URL}/beads/context", params=params, headers=_forward_headers(request))
        response.raise_for_status()
        return response.json()
    except requests.exceptions.RequestException as e:
        raise HTTPException(status_code=502, detail=f"Core Server Error: {str(e)}")

@app.get("/patients")
def get_patients(request: Request):
    try:
        response = requests.get(f"{CORE_URL}/patients", headers=_forward_headers(request))
        response.raise_for_status()
        return response.json()
    except requests.exceptions.RequestException as e:
        raise HTTPException(status_code=502, detail=f"Core Server Error: {str(e)}")

# --- AI Integration ---
import ai

class InsightRequest(BaseModel):
    target_bead_id: str

@app.post("/ai/insight")
def get_insight(body: InsightRequest, request: Request):
    try:
        fwd = _forward_headers(request)

        # 1. Fetch Target Bead
        target_res = requests.get(f"{CORE_URL}/beads", params={"id": body.target_bead_id}, headers=fwd)
        if target_res.status_code != 200:
            raise HTTPException(status_code=404, detail="Target Bead not found")
        target_bead = target_res.json()

        # 2. Fetch Context (Ancestors)
        # Using depth=10 to get a good history for AI
        context_res = requests.get(f"{CORE_URL}/beads/context", params={"id": body.target_bead_id, "depth": 10}, headers=fwd)
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
