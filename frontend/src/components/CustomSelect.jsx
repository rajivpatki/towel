import { useState, useRef, useEffect, useCallback } from 'react'

function CustomSelect({ value, onChange, options, placeholder = 'Select...', label }) {
  const [isOpen, setIsOpen] = useState(false)
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const containerRef = useRef(null)
  const selectedOption = options.find((opt) => opt.value === value)

  const handleToggle = useCallback(() => {
    setIsOpen((prev) => !prev)
    if (!isOpen) {
      const selectedIndex = options.findIndex((opt) => opt.value === value)
      setHighlightedIndex(selectedIndex >= 0 ? selectedIndex : 0)
    }
  }, [isOpen, options, value])

  const handleSelect = useCallback((option) => {
    onChange(option.value)
    setIsOpen(false)
  }, [onChange])

  const handleKeyDown = useCallback((e) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      if (isOpen && highlightedIndex >= 0) {
        handleSelect(options[highlightedIndex])
      } else {
        handleToggle()
      }
    } else if (e.key === 'Escape') {
      setIsOpen(false)
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      if (!isOpen) {
        setIsOpen(true)
        setHighlightedIndex(0)
      } else {
        setHighlightedIndex((prev) => (prev + 1) % options.length)
      }
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      if (!isOpen) {
        setIsOpen(true)
        setHighlightedIndex(options.length - 1)
      } else {
        setHighlightedIndex((prev) => (prev - 1 + options.length) % options.length)
      }
    }
  }, [isOpen, highlightedIndex, options, handleSelect, handleToggle])

  useEffect(() => {
    function handleClickOutside(event) {
      if (containerRef.current && !containerRef.current.contains(event.target)) {
        setIsOpen(false)
      }
    }

    if (isOpen) {
      document.addEventListener('mousedown', handleClickOutside)
      return () => document.removeEventListener('mousedown', handleClickOutside)
    }
  }, [isOpen])

  useEffect(() => {
    if (isOpen && highlightedIndex >= 0) {
      const optionElements = containerRef.current?.querySelectorAll('.custom-select-option')
      optionElements?.[highlightedIndex]?.scrollIntoView({ block: 'nearest' })
    }
  }, [isOpen, highlightedIndex])

  return (
    <div
      ref={containerRef}
      className={`custom-select-container ${isOpen ? 'open' : ''}`}
      onKeyDown={handleKeyDown}
      tabIndex={0}
      role="combobox"
      aria-expanded={isOpen}
      aria-haspopup="listbox"
      aria-label={label || placeholder}
    >
      <div
        className="custom-select-trigger"
        onClick={handleToggle}
        role="button"
        tabIndex={-1}
      >
        <span className={`custom-select-value ${!selectedOption ? 'placeholder' : ''}`}>
          {selectedOption?.label || placeholder}
        </span>
        <svg
          className="custom-select-chevron"
          width="16"
          height="16"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
        >
          <path d="m6 9 6 6 6-6" />
        </svg>
      </div>

      {isOpen && (
        <div
          className="custom-select-dropdown"
          role="listbox"
          aria-label={label || placeholder}
        >
          {options.map((option, index) => (
            <div
              key={option.value}
              className={`custom-select-option ${option.value === value ? 'selected' : ''} ${
                index === highlightedIndex ? 'highlighted' : ''
              }`}
              onClick={() => handleSelect(option)}
              onMouseEnter={() => setHighlightedIndex(index)}
              role="option"
              aria-selected={option.value === value}
            >
              <span className="custom-select-option-text">{option.label}</span>
              {option.value === value && (
                <svg
                  className="custom-select-checkmark"
                  width="14"
                  height="14"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2.5"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                >
                  <path d="M20 6 9 17l-5-5" />
                </svg>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

export default CustomSelect
