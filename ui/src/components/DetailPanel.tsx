import { useState, useEffect, type ReactNode } from 'react';
import { Sparkles, Info, AlertTriangle, Code, ChevronDown, ChevronRight } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import { fetchAIInsight } from '../lib/api';
import type { TimelineItem, Patient, BeadUsed } from '../lib/api';

interface DetailPanelProps {
  selectedItem: TimelineItem | null;
  patient: Patient;
}

export function DetailPanel({ selectedItem }: DetailPanelProps) {
  const [insight, setInsight] = useState<string | null>(null);
  const [beadsUsed, setBeadsUsed] = useState<BeadUsed[]>([]);
  const [loadingInsight, setLoadingInsight] = useState(false);
  const [showRaw, setShowRaw] = useState(false);

  useEffect(() => {
    setInsight(null);
    setBeadsUsed([]);
    setLoadingInsight(false);
    setShowRaw(false);
  }, [selectedItem?.data?.id]);

  const loadInsight = async () => {
    if (!selectedItem?.data?.id) return;

    try {
      setLoadingInsight(true);
      const result = await fetchAIInsight(selectedItem.data.id);
      setInsight(result.insight);
      setBeadsUsed(result.beads_used || []);
    } catch (e: any) {
      console.error('Failed to fetch insight:', e);
      // Check if it's a network error or API error
      if (e?.response?.data?.detail) {
        // API returned an error message
        setInsight(`⚠️ ${e.response.data.detail}`);
      } else if (e?.message?.includes('Network Error') || e?.code === 'ERR_NETWORK') {
        // Network/connection error
        setInsight('⚠️ AI API is not reachable. Please ensure the API server is running on port 8000.');
      } else {
        // Generic error with setup hint
        setInsight('⚠️ AI機能を使用するには、GEMINI_API_KEY の設定が必要です。\n\n1. `api/.env` ファイルを作成\n2. `GEMINI_API_KEY=your_key_here` を設定\n3. Docker を再起動');
      }
      setBeadsUsed([]);
    } finally {
      setLoadingInsight(false);
    }
  };

  if (!selectedItem) {
    return (
      <div className="flex flex-col items-center justify-center h-full p-12 text-center">
        <div className="w-24 h-24 bg-gradient-to-br from-cyan-100 to-blue-100 rounded-full flex items-center justify-center mb-6">
          <Info className="w-12 h-12 text-cyan-600" />
        </div>
        <h3 className="text-xl font-bold text-slate-900 mb-2">Select an Item</h3>
        <p className="text-slate-600 max-w-md">
          Choose an item from the timeline to view details and AI analysis
        </p>
      </div>
    );
  }

  const formatDate = (dateString: string) => {
    return new Date(dateString).toLocaleString('en-US', {
      year: 'numeric',
      month: 'short',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    });
  };

  const renderDetailContent = () => {
    const data = selectedItem.data || {};

    switch (selectedItem.type) {
      case 'encounter':
        return (
          <div className="space-y-4">
            <DetailField label="Date" value={formatDate(selectedItem.date)} />
            <DetailField label="Type" value={data.encounter_type === 'outpatient' ? 'Outpatient' : 'Inpatient'} />
            <DetailField label="Department" value={data.department} />
            <DetailField label="Chief Complaint" value={data.chief_complaint} />
            <DetailField label="Clinical Notes" value={data.clinical_notes} isLong />
          </div>
        );
      case 'medication':
        return (
          <div className="space-y-4">
            <DetailField label="Prescribed Date" value={formatDate(selectedItem.date)} />
            <DetailField label="Medication Name" value={data.medication_name} />
            <DetailField label="Dosage" value={data.dosage} />
            <DetailField label="Frequency" value={data.frequency} />
            <DetailField label="Duration" value={data.duration} />
            <DetailField label="Route" value={data.route} />
            <DetailField label="Reason" value={data.reason} isLong />
          </div>
        );
      case 'observation':
        return (
          <div className="space-y-4">
            <DetailField label="Observation Date" value={formatDate(selectedItem.date)} />
            <DetailField label="Type" value={data.observation_type} />
            <DetailField label="Name" value={data.display_name} />
            <DetailField label="Code" value={data.code} />
            {data.value_quantity && (
              <DetailField label="Value" value={`${data.value_quantity} ${data.value_unit || ''}`} />
            )}
            {data.value_text && <DetailField label="Text Value" value={data.value_text} />}
            <DetailField
              label="Interpretation"
              value={data.interpretation === 'abnormal' ? 'Abnormal' : data.interpretation === 'critical' ? 'Critical' : 'Normal'}
              highlight={data.interpretation !== 'normal'}
            />
          </div>
        );
      case 'diagnostic_report':
        return (
          <div className="space-y-4">
            <DetailField label="Report Date" value={formatDate(selectedItem.date)} />
            <DetailField label="Report Type" value={data.report_type} />
            <DetailField label="Title" value={data.title} />
            <DetailField label="Conclusion" value={data.conclusion} isLong />
            {data.findings && (
                <div className="mt-4">
                    <label className="text-sm font-semibold text-slate-700 block mb-2">Findings / Content</label>
                    <div className="prose prose-sm prose-slate max-w-none bg-slate-50 p-4 rounded-lg border border-slate-200 shadow-inner prose-headings:font-bold prose-h2:text-lg prose-h2:mt-6 prose-h2:mb-3 prose-p:my-2">
                        <ReactMarkdown>{data.findings}</ReactMarkdown>
                    </div>
                </div>
            )}
          </div>
        );
      case 'condition':
        return (
          <div className="space-y-4">
            <DetailField label="Recorded Date" value={formatDate(selectedItem.date)} />
            <DetailField label="Condition Name" value={data.condition_name} />
            <DetailField label="Condition Code" value={data.condition_code} />
            <DetailField label="Severity" value={data.severity} highlight={data.severity === 'severe'} />
            <DetailField label="Clinical Status" value={data.clinical_status} />
            <DetailField label="Notes" value={data.notes} isLong />
          </div>
        );
      case 'procedure':
        return (
            <div className="space-y-4">
                <DetailField label="Procedure Date" value={formatDate(selectedItem.date)} />
                <DetailField label="Procedure Name" value={data.procedure_name} />
                <DetailField label="Status" value={data.status} />
                <DetailField label="Reason" value={data.reason} />
                <DetailField label="Outcome" value={data.outcome} />
            </div>
        );
      case 'immunization':
        return (
            <div className="space-y-4">
                <DetailField label="Immunization Date" value={formatDate(selectedItem.date)} />
                <DetailField label="Vaccine" value={data.vaccine_name} />
                <DetailField label="Status" value={data.status} />
                <DetailField label="Route" value={data.route} />
            </div>
        );
      case 'imaging_study':
        return (
            <div className="space-y-4">
                <DetailField label="Study Date" value={formatDate(selectedItem.date)} />
                <DetailField label="Description" value={data.description} />
                <DetailField label="Modality" value={data.modality} />
                <DetailField label="Series Count" value={data.series_count} />
                <DetailField label="Instance Count" value={data.instance_count} />
            </div>
        );
      default:
        return (
          <div className="space-y-4">
            <DetailField label="Type" value={selectedItem.type} />
            <div className="mt-2 text-sm text-slate-500">
                <p>No specific view for this type.</p>
            </div>
          </div>
        );
    }
  };

  return (
    <div className="p-6 space-y-6">
      <div className="bg-white border-2 border-slate-200 rounded-xl p-6">
        <h3 className="text-lg font-bold text-slate-900 mb-6">Details</h3>
        
        {/* Search Snippet Highlight */}
        {selectedItem.snippet && (
            <div className="mb-6 bg-yellow-50 border border-yellow-200 rounded-lg p-4">
                <div className="flex items-center gap-2 mb-2 text-yellow-800 font-semibold text-sm">
                    <Sparkles className="w-4 h-4" />
                    <span>Search Match Context</span>
                </div>
                <div 
                    className="text-sm text-slate-700 leading-relaxed font-serif"
                    dangerouslySetInnerHTML={{ __html: selectedItem.snippet }}
                />
            </div>
        )}

        {renderDetailContent()}

        {/* All Bead Data Section */}
        <div className="mt-6 pt-4 border-t border-slate-200">
          <h4 className="text-sm font-bold text-slate-800 mb-3">All Bead Data</h4>
          <div className="bg-slate-50 rounded-lg p-4 max-h-96 overflow-y-auto">
            {renderObjectFields(selectedItem.data)}
          </div>
        </div>

        {/* Raw Data Expander */}
        <div className="mt-8 pt-4 border-t border-slate-100">
            <button
                onClick={() => setShowRaw(!showRaw)}
                className="flex items-center gap-2 text-xs font-medium text-slate-400 hover:text-slate-600 transition-colors"
            >
                {showRaw ? <ChevronDown className="w-4 h-4" /> : <ChevronRight className="w-4 h-4" />}
                <Code className="w-4 h-4" />
                {showRaw ? 'Hide Raw Data' : 'View Raw Data'}
            </button>

            {showRaw && (
                <div className="mt-2 bg-slate-900 rounded-lg p-4 overflow-x-auto shadow-inner">
                    <pre className="text-xs font-mono text-cyan-300 leading-relaxed">
                        {JSON.stringify(selectedItem.data, null, 2)}
                    </pre>
                </div>
            )}
        </div>
      </div>

      <div className="bg-gradient-to-br from-cyan-50 to-blue-50 border-2 border-cyan-200 rounded-xl p-6">
        <div className="flex items-center gap-3 mb-4">
          <div className="w-10 h-10 bg-gradient-to-br from-cyan-500 to-blue-600 rounded-lg flex items-center justify-center">
            <Sparkles className="w-6 h-6 text-white" />
          </div>
          <h3 className="text-lg font-bold text-slate-900">AI Medical Insight</h3>
        </div>
        
        {!insight && !loadingInsight && (
          <div className="mt-2">
            <p className="text-slate-600 mb-4">
              Get contextual analysis and clinical insights related to this record using AI.
            </p>
            <button
              onClick={loadInsight}
              className="px-4 py-2 bg-gradient-to-r from-blue-600 to-cyan-600 hover:from-blue-700 hover:to-cyan-700 text-white font-semibold rounded-lg shadow-md transition-all flex items-center gap-2"
            >
              <Sparkles className="w-4 h-4" />
              Generate Analysis
            </button>
          </div>
        )}

        {loadingInsight && (
          <div className="flex items-center gap-2 text-slate-500 animate-pulse mt-4">
            <Sparkles className="w-4 h-4" /> Generating insight...
          </div>
        )}

        {insight && (
          <div className="mt-4 animate-fadeIn">
            <div className="prose prose-sm prose-slate max-w-none text-slate-700 leading-relaxed bg-white/50 p-4 rounded-lg border border-blue-100">
                <ReactMarkdown>{insight}</ReactMarkdown>
            </div>

            {/* Beads Used Section */}
            {beadsUsed.length > 0 && (
              <div className="mt-4 p-3 bg-slate-50 rounded-lg border border-slate-200">
                <h5 className="text-xs font-semibold text-slate-600 mb-2">Context Beads Used ({beadsUsed.length})</h5>
                <div className="max-h-32 overflow-y-auto space-y-1">
                  {beadsUsed.map((bead, idx) => (
                    <div key={bead.id || idx} className="flex items-center gap-2 text-xs text-slate-600 py-1 border-b border-slate-100 last:border-0">
                      <span className="px-1.5 py-0.5 bg-slate-200 text-slate-700 rounded font-mono text-[10px]">
                        {bead.type.replace('fhir_', '')}
                      </span>
                      <span className="flex-1 truncate">{bead.description}</span>
                      <span className="text-slate-400 text-[10px]">{bead.timestamp?.substring(0, 10)}</span>
                    </div>
                  ))}
                </div>
              </div>
            )}

            <div className="mt-4 flex items-start gap-2 p-3 bg-blue-100/50 rounded-lg">
              <AlertTriangle className="w-5 h-5 text-blue-700 flex-shrink-0 mt-0.5" />
              <p className="text-sm text-blue-900">
                This is an AI-generated analysis and is not a substitute for professional medical advice. Always consult a healthcare professional.
              </p>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function DetailField({
  label,
  value,
  isLong = false,
  highlight = false,
}: {
  label: string;
  value: any;
  isLong?: boolean;
  highlight?: boolean;
}) {
  if (value === null || value === undefined || value === '') return null;
  return (
    <div>
      <label className="text-sm font-semibold text-slate-700 block mb-1">{label}</label>
      <p
        className={`${isLong ? 'text-sm leading-relaxed' : 'text-base'} text-slate-900 ${
          highlight ? 'bg-red-50 border-2 border-red-200 rounded-lg px-3 py-2 font-semibold text-red-700' : ''
        }`}
      >
        {String(value)}
      </p>
    </div>
  );
}

// Utility to format field names for display
function formatFieldName(key: string): string {
  return key
    .replace(/_/g, ' ')
    .replace(/([a-z])([A-Z])/g, '$1 $2')
    .split(' ')
    .map(word => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ');
}

// Check if a value looks like base64 encoded binary data
function isLikelyBinaryData(value: any): boolean {
  if (typeof value !== 'string') return false;
  // Base64 encoded data is typically long and has specific character patterns
  if (value.length > 1000 && /^[A-Za-z0-9+/]+=*$/.test(value)) return true;
  return false;
}

// Recursively render an object's properties
function renderObjectFields(obj: any, prefix: string = '', depth: number = 0): ReactNode[] {
  if (depth > 5) return []; // Prevent infinite recursion

  const elements: ReactNode[] = [];

  for (const [key, value] of Object.entries(obj)) {
    // Skip internal/meta fields, binary data, and findings (shown formatted above)
    if (key.startsWith('_') || key === 'id' || key === 'findings') continue;

    const fullKey = prefix ? `${prefix}.${key}` : key;
    const displayLabel = formatFieldName(key);

    if (value === null || value === undefined || value === '') continue;

    if (isLikelyBinaryData(value)) {
      elements.push(
        <div key={fullKey} className="py-2 border-b border-slate-100 last:border-0">
          <label className="text-xs font-medium text-slate-500 block mb-0.5">{displayLabel}</label>
          <p className="text-sm text-slate-400 italic">[Binary data - {(value as string).length} chars]</p>
        </div>
      );
      continue;
    }

    if (Array.isArray(value)) {
      if (value.length === 0) continue;

      elements.push(
        <div key={fullKey} className="py-2 border-b border-slate-100 last:border-0">
          <label className="text-xs font-medium text-slate-500 block mb-1">{displayLabel} ({value.length} items)</label>
          <div className="ml-3 space-y-2">
            {value.map((item, idx) => {
              if (typeof item === 'object' && item !== null) {
                return (
                  <div key={`${fullKey}-${idx}`} className="bg-slate-50 rounded p-2 text-xs">
                    {renderObjectFields(item, `${fullKey}[${idx}]`, depth + 1)}
                  </div>
                );
              } else {
                return (
                  <p key={`${fullKey}-${idx}`} className="text-sm text-slate-800">{String(item)}</p>
                );
              }
            })}
          </div>
        </div>
      );
    } else if (typeof value === 'object') {
      elements.push(
        <div key={fullKey} className="py-2 border-b border-slate-100 last:border-0">
          <label className="text-xs font-medium text-slate-500 block mb-1">{displayLabel}</label>
          <div className="ml-3 bg-slate-50 rounded p-2">
            {renderObjectFields(value, fullKey, depth + 1)}
          </div>
        </div>
      );
    } else {
      elements.push(
        <div key={fullKey} className="py-2 border-b border-slate-100 last:border-0">
          <label className="text-xs font-medium text-slate-500 block mb-0.5">{displayLabel}</label>
          <p className="text-sm text-slate-800 break-words">{String(value)}</p>
        </div>
      );
    }
  }

  return elements;
}
