import sys

def read_file_line_by_line(filename):
    try:
        with open(filename, 'r') as file:
            # firsts = True
            total_cpu = 0.0
            max_cpu = 0.0
            total_memory = 0.0
            max_memory = 0.0
            count = 0
            lines = file.readlines()
            for line in lines[1:]:
                line_split = line.strip().split(" ")
                if float(line_split[5]) == 0.0:
                    continue
                total_cpu += float(line_split[5])
                total_memory += float(line_split[7])
                count += 1
                if float(line_split[5]) > max_cpu:
                    max_cpu = float(line_split[5])
                if float(line_split[7]) > max_memory:
                    max_memory = float(line_split[7])
                # print(float(line_split[5]))
                # print(float(line_split[7]))
            print("max cpu: "+str(max_cpu)+", average cpu: "+str(total_cpu/count)+", max memory: "+str(max_memory)+", average memory: "+str(total_memory/count))
    except FileNotFoundError:
        print(f"Error: The file '{filename}' was not found.")
    except Exception as e:
        print(f"An error occurred: {e}")

if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("Usage: python script.py <filename>")
    else:
        filename = sys.argv[1]
        read_file_line_by_line(filename)