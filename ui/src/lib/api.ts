import axios from 'axios';

const API_BASE_URL = 'http://localhost:8080'; // Core Server
const AI_API_BASE_URL = 'http://localhost:8000'; // AI Server

export const api = axios.create({
  baseURL: API_BASE_URL,
  headers: {
    'Content-Type': 'application/json',
  },
});

export const aiApi = axios.create({
  baseURL: AI_API_BASE_URL,
  headers: {
    'Content-Type': 'application/json',
  },
});

// --- Security Clearance Types ---

export type ViewerRole =
  | 'patient'      // 患者本人
  | 'family'       // 家族
  | 'primary_care' // 主治医
  | 'specialist'   // 専門医
  | 'nurse'        // 看護師
  | 'admin'        // 事務
  | 'insurance'    // 保険会社
  | 'researcher'   // 研究者
  | 'emergency'    // 緊急時オーバーライド
  | 'system';      // システム/AI

export const VIEWER_ROLES: { value: ViewerRole; label: string; labelJa: string }[] = [
  { value: 'patient', label: 'Patient', labelJa: '患者本人' },
  { value: 'family', label: 'Family', labelJa: '家族' },
  { value: 'primary_care', label: 'Primary Care', labelJa: '主治医' },
  { value: 'specialist', label: 'Specialist', labelJa: '専門医' },
  { value: 'nurse', label: 'Nurse', labelJa: '看護師' },
  { value: 'admin', label: 'Admin', labelJa: '事務' },
  { value: 'insurance', label: 'Insurance', labelJa: '保険会社' },
  { value: 'researcher', label: 'Researcher', labelJa: '研究者' },
  { value: 'emergency', label: 'Emergency', labelJa: '緊急時' },
  { value: 'system', label: 'System', labelJa: 'システム' },
];

export interface ClearanceRule {
  id: string;
  bead_id: string;
  denied_roles: ViewerRole[];
  created_by: string;
  created_at: string;
  reason?: string;
  expires_at?: string | null;
}

export interface ViewerContext {
  user_id: string;
  roles: ViewerRole[];
  patient_id?: string;
}

// Current viewer context (global state for simplicity)
let currentViewerRoles: ViewerRole[] = ['system'];

export const setViewerRoles = (roles: ViewerRole[]) => {
  currentViewerRoles = roles;
};

export const getViewerRoles = (): ViewerRole[] => {
  return currentViewerRoles;
};

// Helper to add viewer roles header to requests
const getViewerHeaders = () => ({
  'X-Viewer-Roles': currentViewerRoles.join(','),
});

// --- Type Definitions ---

export interface Bead {
  id: string;
  type: string;
  content: any;
  parents: string[];
  timestamp: string;
}

export interface Patient {
  id: string;
  patient_identifier: string;
  family_name: string;
  given_name: string;
  birth_date: string;
  gender: string;
  snippet?: string; // Search result snippet
}

export interface TimelineItem {
  type: 'encounter' | 'medication' | 'observation' | 'diagnostic_report' | 'condition' | 'procedure' | 'immunization' | 'imaging_study';
  date: string;
  data: any;
  parents: string[];
  snippet?: string; // Search result snippet for this item
}

// --- API Functions ---

const mapBeadToPatient = (bead: Bead): Patient => ({
  id: bead.id,
  patient_identifier: bead.id.substring(0, 8),
  family_name: (bead.content?.name || 'Unknown').split(' ')[0],
  given_name: (bead.content?.name || '').split(' ').slice(1).join(' '),
  birth_date: bead.content?.birthDate || '2000-01-01',
  gender: bead.content?.gender || 'unknown',
  snippet: bead.content?._snippet, // Extract snippet
});

export const fetchAllPatients = async (): Promise<Patient[]> => {
  const response = await api.get<Bead[]>('/patients', {
    headers: getViewerHeaders(),
  });
  return response.data.map(mapBeadToPatient);
};

export const searchPatients = async (query: string, resourceTypes?: string[]): Promise<Patient[]> => {
  console.log(`Searching patients with query: ${query}, resourceTypes: ${resourceTypes?.join(',')}`);
  const params: any = { q: query };
  if (resourceTypes && resourceTypes.length > 0) {
    params.resourceTypes = resourceTypes.join(',');
  }
  const response = await api.get<Bead[]>('/search', {
    params,
    headers: getViewerHeaders(),
  });
  return response.data.map(mapBeadToPatient);
};

export interface ResourceTypeCount {
  resourceType: string;
  patientCount: number;
}

export const fetchResourceCounts = async (): Promise<ResourceTypeCount[]> => {
  const response = await api.get<ResourceTypeCount[]>('/resource-counts');
  return response.data;
};

export const fetchPatientTimeline = async (patientId: string): Promise<TimelineItem[]> => {
  console.log(`Fetching timeline for patient: ${patientId}`);
  const response = await api.get<Bead[]>('/beads/context', {
    params: { id: patientId, depth: 50, lookup: 'reverse' },
    headers: getViewerHeaders(),
  });
  console.log('Raw beads response length:', response.data.length);

  const items: TimelineItem[] = response.data
    .map(bead => mapBeadToTimelineItem(bead))
    .filter((item): item is TimelineItem => item !== null);

  return items.sort((a, b) => new Date(b.date).getTime() - new Date(a.date).getTime());
};

function mapBeadToTimelineItem(bead: Bead): TimelineItem | null {
  const content = bead.content || {};
  const type = bead.type;

  // Handle standard types if they exist
  if (type === 'patient_registration') {
    return {
      type: 'encounter', // Treat as encounter or add new type if allowed
      date: content.birthDate || bead.timestamp,
      data: {
        id: bead.id,
        encounter_date: content.birthDate || bead.timestamp,
        encounter_type: 'Patient Registration',
        display_name: content.name,
        // Add extra fields expected by UI components
        department: 'Registration',
        chief_complaint: 'Initial Registration'
      },
      parents: [],
      snippet: bead.content?._snippet
    };
  }

  if (['encounter', 'medication', 'observation', 'diagnostic_report', 'condition'].includes(type)) {
    return {
      type: type as TimelineItem['type'],
      date: bead.timestamp,
      data: { ...content, id: bead.id },
      parents: bead.parents || [],
      snippet: bead.content?._snippet
    };
  }

  // Handle FHIR types
  if (type === 'fhir_encounter') {
    return {
      type: 'encounter',
      date: content.period?.start || bead.timestamp,
      data: {
        id: bead.id,
        encounter_date: content.period?.start || bead.timestamp,
        department: content.type?.[0]?.text || 'General',
        encounter_type: content.class?.code || 'outpatient',
        chief_complaint: content.type?.[0]?.text || 'Visit',
        clinical_notes: ''
      },
      parents: bead.parents || [],
      snippet: bead.content?._snippet
    };
  }

  if (type === 'fhir_medicationrequest') {
    return {
      type: 'medication',
      date: content.authoredOn || bead.timestamp,
      data: {
        id: bead.id,
        prescribed_date: content.authoredOn || bead.timestamp,
        medication_name: content.medicationCodeableConcept?.text || content.medicationReference?.display || 'Unknown Medication',
        dosage: content.dosageInstruction?.[0]?.text || '',
        frequency: content.dosageInstruction?.[0]?.timing?.repeat?.frequency ? `${content.dosageInstruction[0].timing.repeat.frequency}x` : '',
        reason: ''
      },
      parents: bead.parents || [],
      snippet: bead.content?._snippet
    };
  }

  if (type === 'fhir_observation') {
    return {
      type: 'observation',
      date: content.effectiveDateTime || bead.timestamp,
      data: {
        id: bead.id,
        observation_date: content.effectiveDateTime || bead.timestamp,
        display_name: content.code?.text || 'Observation',
        code: content.code?.coding?.[0]?.code || '',
        value_quantity: content.valueQuantity?.value,
        value_unit: content.valueQuantity?.unit,
        value_text: content.valueString,
        interpretation: content.interpretation?.[0]?.coding?.[0]?.code === 'H' ? 'abnormal' : 'normal' // Simplified logic
      },
       parents: bead.parents || [],
       snippet: bead.content?._snippet
    };
  }

  if (type === 'fhir_condition') {
    return {
      type: 'condition',
      date: content.recordedDate || bead.timestamp,
      data: {
        id: bead.id,
        recorded_date: content.recordedDate || bead.timestamp,
        condition_name: content.code?.text || 'Condition',
        condition_code: content.code?.coding?.[0]?.code || '',
        clinical_status: content.clinicalStatus?.coding?.[0]?.code || 'active',
        severity: ''
      },
      parents: bead.parents || [],
      snippet: bead.content?._snippet
    };
  }

  if (type === 'fhir_documentreference' || type === 'fhir_diagnosticreport') {
    let findings = '';
    
    // Attempt to find attachment data from either DocumentReference (content) or DiagnosticReport (presentedForm)
    let attachmentData = null;
    if (content.content?.[0]?.attachment?.data) {
        attachmentData = content.content[0].attachment.data;
    } else if (content.presentedForm?.[0]?.data) {
        attachmentData = content.presentedForm[0].data;
    }

    // Attempt to decode Base64 content from attachment
    if (attachmentData) {
        try {
            const base64Data = attachmentData;
            // Decode Base64 - handle potential UTF-8 characters
            findings = decodeURIComponent(escape(window.atob(base64Data)));

            // Auto-format headers: Ensure they have newlines and ## markdown
            const headers = [
                'Chief Complaint',
                'History of Present Illness',
                'Social History',
                'Allergies',
                'Medications',
                'Assessment and Plan',
                'Plan'
            ];
            
            // Create a single regex for all headers
            // Matches: [Newline/Start] [Optional Whitespace] [Optional #/+] [Header Text]
            const headerPattern = headers.join('|');
            // Use [\r\n]+ to match CR, LF, or CRLF
            const regex = new RegExp(`([\\r\\n]+|^)\\s*(?:[#*]+\\s*)?(${headerPattern})`, 'gi');
            
            // Replace with: Double Newline + **Header** + Newline (Enforces Bold and spacing)
            findings = findings.replace(regex, '\n\n**$2**\n');

        } catch (e) {
            console.warn('Failed to decode document content', e);
            findings = '(Unable to decode document content)';
        }
    }

    return {
      type: 'diagnostic_report',
      date: content.date || content.effectiveDateTime || bead.timestamp,
      data: {
        ...content, // Pass through original fields (conclusion, text, result, presentedForm)
        id: bead.id,
        report_date: content.date || content.effectiveDateTime || bead.timestamp,
        report_type: type === 'fhir_documentreference' ? 'Clinical Note' : 'Diagnostic Report',
        title: content.type?.coding?.[0]?.display || content.code?.text || 'Report',
        // Preserve original conclusion if exists, otherwise fallback to empty if needed (but better to let it be undefined)
        // conclusion: content.conclusion || '', 
        findings: findings // Decoded content (for backward compatibility / convenience)
      },
      parents: bead.parents || [],
      snippet: bead.content?._snippet
    };
  }

  // --- New Types Support ---

  if (type === 'fhir_procedure') {
    return {
        type: 'procedure',
        date: content.performedDateTime || content.performedPeriod?.start || bead.timestamp,
        data: {
            id: bead.id,
            procedure_date: content.performedDateTime || content.performedPeriod?.start || bead.timestamp,
            procedure_name: content.code?.text || content.code?.coding?.[0]?.display || 'Procedure',
            status: content.status || 'completed',
            reason: content.reasonCode?.[0]?.text || '',
            outcome: content.outcome?.text || ''
        },
        parents: bead.parents || [],
        snippet: bead.content?._snippet
    };
  }

  if (type === 'fhir_immunization') {
    return {
        type: 'immunization',
        date: content.occurrenceDateTime || bead.timestamp,
        data: {
            id: bead.id,
            immunization_date: content.occurrenceDateTime || bead.timestamp,
            vaccine_name: content.vaccineCode?.text || content.vaccineCode?.coding?.[0]?.display || 'Vaccination',
            status: content.status || 'completed',
            route: content.route?.text || ''
        },
        parents: bead.parents || [],
        snippet: bead.content?._snippet
    };
  }

  if (type === 'fhir_imagingstudy') {
    return {
        type: 'imaging_study',
        date: content.started || bead.timestamp,
        data: {
             id: bead.id,
             study_date: content.started || bead.timestamp,
             modality: content.modality?.[0]?.code || 'Unknown',
             description: content.description || 'Imaging Study',
             series_count: content.numberOfSeries || 0,
             instance_count: content.numberOfInstances || 0
        },
        parents: bead.parents || [],
        snippet: bead.content?._snippet
    };
  }

  return null;
}

export interface BeadUsed {
  id: string;
  type: string;
  timestamp: string;
  description: string;
}

export interface AIInsightResponse {
  insight: string;
  beads_used: BeadUsed[];
}

export const fetchAIInsight = async (targetBeadId: string): Promise<AIInsightResponse> => {
  const response = await aiApi.post<AIInsightResponse>('/ai/insight', {
    target_bead_id: targetBeadId
  });
  return response.data;
};

// --- Clearance API Functions ---

export const fetchClearanceRules = async (beadId: string): Promise<ClearanceRule[]> => {
  const response = await api.get<ClearanceRule[]>('/clearance', {
    params: { bead_id: beadId },
    headers: getViewerHeaders(),
  });
  return response.data || [];
};

export interface CreateClearanceRequest {
  bead_id: string;
  denied_roles: ViewerRole[];
  reason?: string;
  expires_at?: string | null;
}

export const createClearanceRule = async (request: CreateClearanceRequest): Promise<ClearanceRule> => {
  const response = await api.post<ClearanceRule>('/clearance', request, {
    headers: getViewerHeaders(),
  });
  return response.data;
};

export const deleteClearanceRule = async (ruleId: string): Promise<void> => {
  await api.delete('/clearance', {
    params: { id: ruleId },
    headers: getViewerHeaders(),
  });
};

export const checkAccess = async (beadId: string): Promise<boolean> => {
  const response = await api.get<{ has_access: boolean }>('/clearance/check', {
    params: { bead_id: beadId },
    headers: getViewerHeaders(),
  });
  return response.data.has_access;
};

export const fetchAvailableRoles = async (): Promise<ViewerRole[]> => {
  const response = await api.get<ViewerRole[]>('/roles');
  return response.data;
};
