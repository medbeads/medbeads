import google.generativeai as genai
import os
from typing import List, Dict, Any

# Configure Gemini API
API_KEY = os.environ.get("GEMINI_API_KEY")
if API_KEY:
    API_KEY = API_KEY.strip() # Remove potential whitespace/newlines
    print(f"🔑 Loaded API Key: {API_KEY[:4]}...**** (Length: {len(API_KEY)})")
    genai.configure(api_key=API_KEY)
else:
    print("❌ No API Key found in environment variables.")

def get_fhir_date(bead: Dict[str, Any]) -> str:
    """
    Extract the proper clinical date from FHIR content.
    Falls back to bead timestamp if no FHIR date found.
    """
    content = bead.get("content", {})
    bead_type = bead.get("type", "")
    fallback = bead.get("timestamp", "unknown")

    # FHIR DocumentReference and DiagnosticReport
    if bead_type in ("fhir_documentreference", "fhir_diagnosticreport"):
        return content.get("date") or content.get("effectiveDateTime") or fallback

    # FHIR Encounter
    if bead_type == "fhir_encounter":
        period = content.get("period", {})
        return period.get("start") or fallback

    # FHIR MedicationRequest
    if bead_type == "fhir_medicationrequest":
        return content.get("authoredOn") or fallback

    # FHIR Observation
    if bead_type == "fhir_observation":
        return content.get("effectiveDateTime") or fallback

    # FHIR Condition
    if bead_type == "fhir_condition":
        return content.get("recordedDate") or content.get("onsetDateTime") or fallback

    # FHIR Procedure
    if bead_type == "fhir_procedure":
        return content.get("performedDateTime") or content.get("performedPeriod", {}).get("start") or fallback

    # FHIR Immunization
    if bead_type == "fhir_immunization":
        return content.get("occurrenceDateTime") or fallback

    return fallback

def format_context(beads: List[Dict[str, Any]]) -> str:
    """
    Formats the list of beads (DAG context) into a chronological text for the LLM.
    Assumes beads are already sorted or we sort them here by timestamp.
    """
    # Sort by FHIR date (not bead timestamp)
    sorted_beads = sorted(beads, key=lambda x: get_fhir_date(x))

    text_parts = []
    for b in sorted_beads:
        b_type = b.get("type", "unknown")
        timestamp = get_fhir_date(b)
        content = b.get("content", {})
        
        # Simplified string representation
        text_parts.append(f"[{timestamp}] Type: {b_type}")
        text_parts.append(f"Content: {content}")
        text_parts.append("---")
        
    return "\n".join(text_parts)

def generate_insight(target_bead: Dict[str, Any], context_beads: List[Dict[str, Any]]) -> Dict[str, Any]:
    """
    Generates a clinical insight for the target bead based on the provided context.
    Returns dict with 'insight' and 'beads_used'.
    """
    # Sort context beads by FHIR date (not bead timestamp)
    sorted_context = sorted(context_beads, key=lambda x: get_fhir_date(x))

    if not API_KEY:
        return {
            "insight": "⚠️ Gemini API Key is not set. Please set GEMINI_API_KEY environment variable.",
            "beads_used": []
        }

    model = genai.GenerativeModel('gemini-3-pro-preview')

    context_text = format_context(sorted_context)
    target_info = f"Type: {target_bead.get('type')}\nContent: {target_bead.get('content')}"

    # Build beads_used list with summary info
    beads_used = []
    for b in sorted_context:
        bead_summary = {
            "id": b.get("id", "unknown"),
            "type": b.get("type", "unknown"),
            "timestamp": get_fhir_date(b)
        }
        # Add a brief description based on type
        content = b.get("content", {})
        if b.get("type") == "fhir_observation":
            bead_summary["description"] = content.get("code", {}).get("text", "Observation")
        elif b.get("type") == "fhir_condition":
            bead_summary["description"] = content.get("code", {}).get("text", "Condition")
        elif b.get("type") == "fhir_medicationrequest":
            bead_summary["description"] = content.get("medicationCodeableConcept", {}).get("text", "Medication")
        elif b.get("type") == "fhir_encounter":
            bead_summary["description"] = content.get("type", [{}])[0].get("text", "Encounter") if content.get("type") else "Encounter"
        elif b.get("type") == "fhir_procedure":
            bead_summary["description"] = content.get("code", {}).get("text", "Procedure")
        elif b.get("type") == "fhir_immunization":
            bead_summary["description"] = content.get("vaccineCode", {}).get("text", "Immunization")
        elif b.get("type") == "fhir_documentreference" or b.get("type") == "fhir_diagnosticreport":
            bead_summary["description"] = content.get("type", {}).get("coding", [{}])[0].get("display", "Report") if content.get("type") else "Report"
        elif b.get("type") == "patient_registration":
            bead_summary["description"] = content.get("name", "Patient")
        else:
            bead_summary["description"] = b.get("type", "Unknown")
        beads_used.append(bead_summary)

    prompt = f"""
You are an expert Clinical AI assistant embedded in an Electronic Medical Record system.
Your goal is to analyze a specific medical event (Target Event) in the context of the patient's history (Context).

### Context (Chronological History):
{context_text}

### Target Event (Focus of Analysis):
{target_info}

### Instructions:
1. Analyze the Target Event in the context of patient history.
2. Identify correlations, contradictions, or patterns.
3. **Format**: Use the following structure exactly.

   ### Output Structure:

   **Summary**: (One concise sentence summarizing the event)

   **Key Insights**
   - 🔍 **Correlation**: (Analyze relationship with past history, use [[brackets]] for key terms)
   - 💊 **Medication/Treatment**: (Analyze prescriptions or procedures)
   - ⚠️ **Risk/Alert**: (Identify potential risks or "None" if safe)

4. **Hyperlinking**: If you mention a specific medication, condition, or observation from the context, wrap the term in double brackets, e.g., [[Tamiflu]] or [[Diabetes]].

### Insight:
"""
    
    try:
        response = model.generate_content(prompt)
        return {
            "insight": response.text,
            "beads_used": beads_used
        }
    except Exception as e:
        return {
            "insight": f"❌ AI Inference Failed: {str(e)}",
            "beads_used": beads_used
        }
