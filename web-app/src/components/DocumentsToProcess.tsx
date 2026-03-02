import React from "react";
import { Document } from "../DocumentProcessor";
import DocumentCard from "./DocumentCard";
import ArrowPathIcon from "@heroicons/react/24/outline/ArrowPathIcon";

export interface DocumentsToProcessProps {
  documents: Document[];
  generateTitles?: boolean;
  setGenerateTitles?: React.Dispatch<React.SetStateAction<boolean>>;
  generateTags?: boolean;
  setGenerateTags?: React.Dispatch<React.SetStateAction<boolean>>;
  generateCorrespondents?: boolean;
  setGenerateCorrespondents?: React.Dispatch<React.SetStateAction<boolean>>;
  generateCreatedDate?: boolean;
  setGenerateCreatedDate?: React.Dispatch<React.SetStateAction<boolean>>;
  generateCustomFields?: boolean;
  setGenerateCustomFields?: React.Dispatch<React.SetStateAction<boolean>>;
  generateSummary?: boolean;
  setGenerateSummary?: React.Dispatch<React.SetStateAction<boolean>>;
  onProcess?: () => void;
  processing?: boolean;
  onReload: () => void;
  selectedDocuments?: number[];
  onSelectDocument?: (documentId: number) => void;
  gridCols?: string;
}

const DocumentsToProcess: React.FC<DocumentsToProcessProps> = ({
  documents,
  generateTitles,
  setGenerateTitles,
  generateTags,
  setGenerateTags,
  generateCorrespondents,
  setGenerateCorrespondents,
  generateCreatedDate,
  setGenerateCreatedDate,
  generateCustomFields,
  setGenerateCustomFields,
  generateSummary,
  setGenerateSummary,
  onProcess,
  processing = false,
  onReload,
  selectedDocuments,
  onSelectDocument,
  gridCols = "2",
}) => (
  <section>
    <div className="flex justify-between items-center mb-6">
      <h2 className="text-2xl font-semibold text-gray-700 dark:text-gray-200">Documents to Process</h2>
      <div className="flex space-x-2">
        <button
          onClick={onReload}
          disabled={processing}
          className="bg-blue-600 text-white dark:bg-blue-800 dark:text-gray-200 px-4 py-2 rounded hover:bg-blue-700 dark:hover:bg-blue-900 focus:outline-none"
        >
          <ArrowPathIcon className="h-5 w-5" />
        </button>
        {onProcess && (
          <button
            onClick={onProcess}
            disabled={processing}
            className="bg-blue-600 text-white dark:bg-blue-800 dark:text-gray-200 px-4 py-2 rounded hover:bg-blue-700 dark:hover:bg-blue-900 focus:outline-none"
          >
            {processing ? "Processing..." : "Generate Suggestions"}
          </button>
        )}
      </div>
    </div>

    <div className="flex space-x-4 mb-6">
      {setGenerateTitles && generateTitles !== undefined && (
        <label className="flex items-center space-x-2">
          <input
            type="checkbox"
            checked={generateTitles}
            onChange={(e) => setGenerateTitles(e.target.checked)}
            className="dark:bg-gray-700 dark:border-gray-600"
          />
          <span className="text-gray-700 dark:text-gray-200">Generate Titles</span>
        </label>
      )}
      {setGenerateTags && generateTags !== undefined && (
        <label className="flex items-center space-x-2">
          <input
            type="checkbox"
            checked={generateTags}
            onChange={(e) => setGenerateTags(e.target.checked)}
            className="dark:bg-gray-700 dark:border-gray-600"
          />
          <span className="text-gray-700 dark:text-gray-200">Generate Tags</span>
        </label>
      )}
      {setGenerateCorrespondents && generateCorrespondents !== undefined && (
        <label className="flex items-center space-x-2">
          <input
            type="checkbox"
            checked={generateCorrespondents}
            onChange={(e) => setGenerateCorrespondents(e.target.checked)}
            className="dark:bg-gray-700 dark:border-gray-600"
          />
          <span className="text-gray-700 dark:text-gray-200">Generate Correspondents</span>
        </label>
      )}
      {setGenerateCreatedDate && generateCreatedDate !== undefined && (
        <label className="flex items-center space-x-2">
          <input
            type="checkbox"
            checked={generateCreatedDate}
            onChange={(e) => setGenerateCreatedDate(e.target.checked)}
            className="dark:bg-gray-700 dark:border-gray-600"
          />
          <span className="text-gray-700 dark:text-gray-200">Generate Created Date</span>
        </label>
      )}
      {setGenerateCustomFields && generateCustomFields !== undefined && (
        <label className="flex items-center space-x-2">
          <input
            type="checkbox"
            checked={generateCustomFields}
            onChange={(e) => setGenerateCustomFields(e.target.checked)}
            className="dark:bg-gray-700 dark:border-gray-600"
          />
          <span className="text-gray-700 dark:text-gray-200">Generate Custom Fields</span>
        </label>
      )}
      {setGenerateSummary && generateSummary !== undefined && (
        <label className="flex items-center space-x-2">
          <input
            type="checkbox"
            checked={generateSummary}
            onChange={(e) => setGenerateSummary(e.target.checked)}
            className="dark:bg-gray-700 dark:border-gray-600"
          />
          <span className="text-gray-700 dark:text-gray-200">Generate Summary</span>
        </label>
      )}
    </div>

    <div className={`grid grid-cols-1 md:grid-cols-${gridCols} gap-4`}>
      {documents.map((doc) => (
        <DocumentCard
          key={doc.id}
          document={doc}
          isSelected={selectedDocuments ? selectedDocuments.includes(doc.id) : true}
          onSelect={onSelectDocument}
        />
      ))}
    </div>
  </section>
);

export default DocumentsToProcess;
