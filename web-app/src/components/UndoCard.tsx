// UndoCard.tsx
import React from 'react';
import { Tooltip } from 'react-tooltip';
import Button from './ui/Button';

interface ModificationProps {
  ID: number;
  DocumentID: number;
  DateChanged: string;
  ModField: string;
  PreviousValue: string;
  NewValue: string;
  Undone: boolean;
  UndoneDate: string | null;
  onUndo: (id: number) => void;
  paperlessUrl: string;
}

const formatDate = (dateString: string | null): string => {
  if (!dateString) return '';

  try {
    const date = new Date(dateString);
    // Check if date is valid
    if (isNaN(date.getTime())) {
      return 'Invalid date';
    }
    return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')} ${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
  } catch {
    return 'Invalid date';
  }
};

const buildPaperlessUrl = (paperlessUrl: string, documentId: number): string => {
  return `${paperlessUrl}/documents/${documentId}/details`;
};

const shouldWrapValue = (field: string): boolean => {
  return field.toLowerCase() === 'tags' || field.toLowerCase() === 'summary';
};

const valueLayoutClass = (field: string): string => {
  if (field.toLowerCase() === 'tags') {
    return 'break-all whitespace-normal [overflow-wrap:anywhere]';
  }
  if (field.toLowerCase() === 'summary') {
    return 'break-words whitespace-pre-wrap [overflow-wrap:anywhere]';
  }
  return 'truncate overflow-hidden whitespace-nowrap';
};

const UndoCard: React.FC<ModificationProps> = ({
  ID,
  DocumentID,
  DateChanged,
  ModField,
  PreviousValue,
  NewValue,
  Undone,
  UndoneDate,
  onUndo,
  paperlessUrl,
}) => {
  const formatValue = (value: string, field: string) => {
    if (field === 'tags') {
      try {
        const tags = JSON.parse(value) as string[];
        return (
          <div className="flex min-w-0 flex-wrap gap-1">
            {tags.map((tag) => (
              <span
                key={tag}
                className="max-w-full break-all rounded-full bg-blue-100 px-2.5 py-0.5 text-xs font-medium text-blue-800 dark:bg-blue-900 dark:text-blue-200"
              >
                {tag}
              </span>
            ))}
          </div>
        );
      } catch {
        return value;
      }
    } else if (field.toLowerCase().includes('date')) {
      return formatDate(value);
    }
    return value;
  };

  return (
    <article className="undo-card relative min-w-0 rounded-md border border-line bg-surface p-4 shadow-card">
      <div className="flex min-w-0 flex-col gap-4 sm:grid sm:grid-cols-[minmax(0,1fr)_auto] sm:items-stretch">
        <div className="undo-card__content min-w-0">
          <div className="undo-card__metadata mb-4 grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-3 sm:gap-4">
            <div className="min-w-0">
              <div className="text-xs uppercase text-gray-500 dark:text-gray-400 font-semibold mb-1">
                Date Modified
              </div>
              <div className="break-words text-sm text-gray-700 dark:text-gray-300">
                {DateChanged && formatDate(DateChanged)}
              </div>
            </div>
            <div className="min-w-0">
              <a
                href={buildPaperlessUrl(paperlessUrl, DocumentID)}
                target="_blank"
                rel="noopener noreferrer"
                className="block min-w-0 text-blue-500 hover:text-blue-600 dark:text-blue-400 dark:hover:text-blue-300"
              >
                <div className="text-xs uppercase text-gray-500 dark:text-gray-400 font-semibold mb-1">
                  Document ID
                </div>
                <div className="text-sm text-gray-700 dark:text-gray-300">
                  {DocumentID}
                </div>
              </a>
            </div>

            <div className="min-w-0">
              <div className="text-xs uppercase text-gray-500 dark:text-gray-400 font-semibold mb-1">
                Modified Field
              </div>
              <div className="break-words text-sm text-gray-700 dark:text-gray-300">
                {ModField}
              </div>
            </div>
          </div>
          <div className="undo-card__values min-w-0">
            <div className="space-y-2">
              <div className={`grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-1 text-sm ${Undone ? 'line-through' : ''}`}>
                <span className="text-red-500 dark:text-red-400">Previous:</span>
                <span
                  className={`group relative min-w-0 max-w-full text-gray-600 dark:text-gray-300 ${valueLayoutClass(ModField)}`}
                  { // Add a tooltip when a single-line value is too long.
                    ...(!shouldWrapValue(ModField) && PreviousValue.length > 100 ? {
                    'data-tooltip-id': `tooltip-${ID}-prev`
                  } : {})}
                >
                  {formatValue(PreviousValue, ModField)}
                </span>
              </div>
              <div className={`grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-1 text-sm ${Undone ? 'line-through' : ''}`}>
                <span className="text-green-500 dark:text-green-400">New:</span>
                <span
                  className={`group relative min-w-0 max-w-full text-gray-600 dark:text-gray-300 ${valueLayoutClass(ModField)}`}
                  { // Add a tooltip when a single-line value is too long.
                    ...(!shouldWrapValue(ModField) && NewValue.length > 100 ? {
                    'data-tooltip-id': `tooltip-${ID}-new`
                  } : {})}
                >
                  {formatValue(NewValue, ModField)}
                </span>
              </div>
            </div>
            <Tooltip 
              id={`tooltip-${ID}-prev`} 
              place="bottom"
              className="flex-wrap"
              style={{
                flexWrap: 'wrap',
                wordWrap: 'break-word',
                zIndex: 10,
                whiteSpace: 'pre-line',
                textAlign: 'left',
              }}
            >
              {PreviousValue}
            </Tooltip>
            <Tooltip 
              id={`tooltip-${ID}-new`} 
              place="bottom"
              className="flex-wrap"
              style={{
                flexWrap: 'wrap',
                wordWrap: 'break-word',
                zIndex: 10,
                whiteSpace: 'pre-line',
                textAlign: 'left',
              }}
            >
              {NewValue}
            </Tooltip>
          </div>
        </div>
        <div className="undo-card__action flex min-w-0 items-stretch border-t border-line pt-4 sm:items-center sm:border-l sm:border-t-0 sm:pl-4 sm:pt-0">
          <Button
            type="button"
            onClick={() => onUndo(ID)}
            disabled={Undone}
            variant={Undone ? 'secondary' : 'primary'}
            className="h-auto min-h-9 w-full break-words py-2 text-center sm:w-40"
            aria-label={Undone ? `Undone on ${formatDate(UndoneDate)}` : `Undo ${ModField} change for document ${DocumentID}`}
          >
            {Undone ? (
              <span className="flex min-w-0 flex-col items-center leading-tight">
                <span className="block text-xs">Undone on</span>
                <span className="block text-xs">{formatDate(UndoneDate)}</span>
              </span>
            ) : (
              'Undo'
            )}
          </Button>
        </div>
      </div>
    </article>
  );
};

export default UndoCard;
