import { type Bead } from '../lib/api';
import { Pill, Activity, Stethoscope, FileText, Calendar, AlertCircle } from 'lucide-react';
import clsx from 'clsx';

interface TimelineProps {
  beads: Bead[];
  onSelect: (bead: Bead) => void;
  selectedId?: string;
}

// Map resource types to icons and colors
const getTypeConfig = (type: string) => {
  switch (type.toLowerCase()) {
    case 'medicationrequest':
      return { icon: Pill, color: 'text-purple-400', bg: 'bg-purple-400/10', label: 'Medication' };
    case 'observation':
      return { icon: Activity, color: 'text-emerald-400', bg: 'bg-emerald-400/10', label: 'Observation' };
    case 'encounter':
    case 'fhir_encounter':
      return { icon: Stethoscope, color: 'text-blue-400', bg: 'bg-blue-400/10', label: 'Encounter' };
    case 'condition':
      return { icon: AlertCircle, color: 'text-red-400', bg: 'bg-red-400/10', label: 'Condition' };
    case 'patient':
    case 'patient_registration':
      return { icon: Calendar, color: 'text-gray-400', bg: 'bg-gray-400/10', label: 'Patient History' };
    default:
      return { icon: FileText, color: 'text-gray-300', bg: 'bg-gray-500/10', label: type };
  }
};

export default function Timeline({ beads, onSelect, selectedId }: TimelineProps) {
  // 1. Sort beads by timestamp (Newest first)
  const sorted = [...beads].sort((a, b) => 
    new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime()
  );

  // 2. Group by Date (YYYY-MM-DD)
  const groups = sorted.reduce((acc, bead) => {
    const date = bead.timestamp.split('T')[0];
    if (!acc[date]) acc[date] = [];
    acc[date].push(bead);
    return acc;
  }, {} as Record<string, Bead[]>);

  // Sort dates descending
  const dates = Object.keys(groups).sort((a, b) => new Date(b).getTime() - new Date(a).getTime());

  return (
    <div className="relative border-l border-[var(--glass-border)] ml-6 space-y-8">
      {dates.map((date) => (
        <div key={date} className="relative pl-6">
          {/* Date Marker */}
          <div className="absolute -left-[33px] top-0 flex items-center justify-center w-4 h-4 rounded-full bg-[hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))] shadow-[0_0_10px_hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))]"></div>
          <h3 className="text-lg font-bold mb-4 opacity-90">{date}</h3>

          <div className="space-y-4">
            {groups[date].map((bead) => {
              const config = getTypeConfig(bead.type);
              const Icon = config.icon;

              const isSelected = selectedId === bead.id;
              
              return (
                <div 
                  key={bead.id} 
                  onClick={() => onSelect(bead)}
                  className={clsx(
                    "relative p-4 rounded-lg border transition-all cursor-pointer group",
                    isSelected 
                      ? "bg-white/10 border-[hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))] shadow-[0_0_15px_hsla(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l),0.2)]"
                      : "border-[var(--glass-border)] hover:bg-white/5",
                    config.bg
                  )}
                >
                  <div className="flex items-start gap-3">
                    <div className={clsx(
                      "p-2 rounded-lg bg-black/20 transition-colors", 
                      isSelected ? "text-white bg-[hsl(var(--color-primary-h),var(--color-primary-s),var(--color-primary-l))]" : config.color
                    )}>
                      <Icon size={20} />
                    </div>
                    
                    <div className="flex-1 min-w-0">
                      <div className="flex justify-between items-start">
                        <span className={clsx("text-sm font-semibold uppercase tracking-wider", config.color)}>
                          {config.label}
                        </span>
                        <span className="text-xs opacity-40 font-mono" title="Bead Hash ID">
                          {(bead.id || "").substring(0, 8)}...
                        </span>
                      </div>
                      
                      {/* Content Rendering based on type */}
                      <div className="mt-2 text-sm opacity-90 leading-relaxed">
                        <BeadContent content={bead.content} type={bead.type} />
                      </div>
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      ))}
    </div>
  );
}

// Helper to render content nicely
const BeadContent = ({ content, type }: { content: any, type: string }) => {
  // Flatten content if needed or pick specific fields
  if (type.includes('Medication')) {
    return (
      <div>
        <div className="font-bold text-lg">{content.medicationCodeableConcept?.text}</div>
        <div className="opacity-70 mt-1">{content.dosageInstruction?.[0]?.text}</div>
      </div>
    );
  }
  if (type.includes('Observation')) {
    return (
      <div className="flex items-baseline gap-2">
        <span className="opacity-70">{content.code?.text}:</span>
        <span className="font-bold text-lg">
          {content.valueQuantity?.value} {content.valueQuantity?.unit}
        </span>
      </div>
    );
  }
  if (type.includes('Encounter')) {
    return (
      <div>
        <div className="font-bold">{content.type?.[0]?.text || "Medical Encounter"}</div>
        {content.hospitalization && <div className="text-xs mt-1 text-orange-300">Hospitalized</div>}
      </div>
    );
  }
  if (type.includes('Condition')) {
    return (
       <div className="font-bold text-red-200">{content.code?.text}</div>
    );
  }

  // Fallback
  return <pre className="text-xs opacity-60 overflow-hidden text-ellipsis">{JSON.stringify(content).slice(0, 100)}</pre>;
};
