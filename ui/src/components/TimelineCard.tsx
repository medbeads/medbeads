import { Calendar, Pill, Activity, FileText, AlertCircle } from 'lucide-react';
import type { TimelineItem } from '../lib/api';

interface TimelineCardProps {
  item: TimelineItem;
  isSelected: boolean;
  onClick: () => void;
}

export function TimelineCard({ item, isSelected, onClick }: TimelineCardProps) {
  const formatDate = (dateString: string) => {
    const date = new Date(dateString);
    return {
      date: date.toLocaleDateString('en-US', {
        year: 'numeric',
        month: 'short',
        day: '2-digit',
      }),
      time: date.toLocaleTimeString('en-US', {
        hour: '2-digit',
        minute: '2-digit',
      }),
    };
  };

  const getCardContent = () => {
    const data = item.data || {};
    
    switch (item.type) {
      case 'encounter':
        return {
          icon: Calendar,
          color: 'blue',
          title: 'Encounter',
          subtitle: data.department || 'General',
          content: data.chief_complaint || 'Regular Checkup',
          badge: data.encounter_type === 'outpatient' ? 'Outpatient' : 'Inpatient',
        };
      case 'medication':
        return {
          icon: Pill,
          color: 'green',
          title: 'Medication',
          subtitle: data.medication_name || 'Unknown',
          content: `${data.dosage || ''} / ${data.frequency || ''}`,
          badge: data.duration || 'Short-term',
        };
      case 'observation':
        return {
          icon: Activity,
          color: 'purple',
          title: 'Observation',
          subtitle: data.display_name || 'Observation',
          content: data.value_quantity
            ? `${data.value_quantity} ${data.value_unit || ''}`
            : data.value_text || '',
          badge:
            data.interpretation === 'abnormal'
              ? 'Abnormal'
              : data.interpretation === 'critical'
              ? 'Critical'
              : 'Normal',
          isAbnormal: data.interpretation !== 'normal',
        };
      case 'diagnostic_report':
        return {
          icon: FileText,
          color: 'orange',
          title: 'Diagnostic Report',
          subtitle: data.title || 'Report',
          content: data.conclusion || 'No conclusion',
          badge: data.report_type === 'radiology' ? 'Radiology' : 'Report',
        };
      case 'condition':
        return {
          icon: AlertCircle,
          color: 'red',
          title: 'Condition',
          subtitle: data.condition_name || 'Condition',
          content: data.condition_code || '',
          badge:
            data.severity === 'severe'
              ? 'Severe'
              : data.severity === 'moderate'
              ? 'Moderate'
              : 'Mild',
        };
      default:
        return {
          icon: Activity,
          color: 'blue',
          title: item.type,
          subtitle: 'Event',
          content: '',
          badge: 'Info',
        };
    }
  };

  const cardData = getCardContent();
  const Icon = cardData.icon;
  const { date, time } = formatDate(item.date);

  const colorClasses: Record<string, any> = {
    blue: {
      bg: 'bg-blue-50',
      border: 'border-blue-200',
      icon: 'bg-blue-600',
      badge: 'bg-blue-100 text-blue-700',
      selected: 'border-blue-500 shadow-blue-200',
    },
    green: {
      bg: 'bg-green-50',
      border: 'border-green-200',
      icon: 'bg-green-600',
      badge: 'bg-green-100 text-green-700',
      selected: 'border-green-500 shadow-green-200',
    },
    purple: {
      bg: 'bg-purple-50',
      border: 'border-purple-200',
      icon: 'bg-purple-600',
      badge: (cardData as any).isAbnormal
        ? 'bg-red-100 text-red-700'
        : 'bg-purple-100 text-purple-700',
      selected: 'border-purple-500 shadow-purple-200',
    },
    orange: {
      bg: 'bg-orange-50',
      border: 'border-orange-200',
      icon: 'bg-orange-600',
      badge: 'bg-orange-100 text-orange-700',
      selected: 'border-orange-500 shadow-orange-200',
    },
    red: {
      bg: 'bg-red-50',
      border: 'border-red-200',
      icon: 'bg-red-600',
      badge: 'bg-red-100 text-red-700',
      selected: 'border-red-500 shadow-red-200',
    },
  };

  const colors = colorClasses[cardData.color] || colorClasses.blue;

  return (
    <button
      onClick={onClick}
      className={`w-full text-left p-4 rounded-xl border-2 transition-all duration-200 ${
        colors.bg
      } ${colors.border} ${
        isSelected
          ? `${colors.selected} shadow-lg scale-[1.02]`
          : 'hover:shadow-md hover:scale-[1.01]'
      }`}
    >
      <div className="flex items-center gap-2 mb-3 text-sm font-bold text-slate-700">
        <span>{date}</span>
        <span className="text-slate-400">・</span>
        <span>{time}</span>
      </div>
      <div className="flex items-start gap-4">
        <div className={`flex-shrink-0 w-12 h-12 ${colors.icon} rounded-xl flex items-center justify-center`}>
          <Icon className="w-6 h-6 text-white" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-2">
            <span className="text-xs font-semibold text-slate-500">{cardData.title}</span>
            <span className={`text-xs font-medium px-2 py-1 rounded ${colors.badge}`}>
              {cardData.badge}
            </span>
          </div>
          <h3 className="text-lg font-bold text-slate-900 mb-1">{cardData.subtitle}</h3>
          <p className="text-sm text-slate-600 line-clamp-2">{cardData.content}</p>
        </div>
      </div>
    </button>
  );
}
